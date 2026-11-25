// Package server is the command server: a job queue in SQLite with three ways
// in — the boards' poll API, the REST submit API and MCP — all calling Service.
package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/RolandWarburton/ptouch-print/internal/label"
	"github.com/RolandWarburton/ptouch-print/internal/store"
	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

const (
	// OfflineAfter is how long since a board's last poll before its submits are
	// refused. The board polls every 15 s, so this is four missed polls.
	OfflineAfter = 60 * time.Second
	// Retention is how long jobs are kept.
	Retention = 365 * 24 * time.Hour

	defaultListLimit = 20
	maxListLimit     = 100
)

// Error is a refusal a caller can act on: an HTTP status, a code and a reason.
type Error struct {
	Status int    `json:"-"`
	Code   string `json:"code"`
	Msg    string `json:"error"`
}

func (e *Error) Error() string { return e.Msg }

func refuse(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Msg: fmt.Sprintf(format, args...)}
}

type Service struct {
	store    *store.Store
	printers []*printerState
	log      *slog.Logger
	now      func() time.Time

	mu sync.Mutex // guards what polls change in printers
}

func NewService(st *store.Store, printers []Printer, log *slog.Logger) *Service {
	s := &Service{store: st, log: log, now: time.Now}
	for _, p := range printers {
		s.printers = append(s.printers, &printerState{Printer: p})
	}
	return s
}

// Next records a printer's poll and hands its board that printer's oldest
// queued job, if any, even while the printer has a problem: that job fails now
// rather than printing later, unexpectedly.
func (s *Service) Next(ctx context.Context, printer string, poll Poll) (*wire.Job, error) {
	p, err := s.printer(printer)
	if err != nil {
		return nil, err
	}
	// Media the server doesn't know can't be checked against a job, so it's a
	// problem too. Either media or problem is set, never both.
	var media wire.Media
	problem := poll.Problem
	if problem == "" {
		var ok bool
		if media, ok = wire.FindMedia(p.Family, poll.Media); !ok {
			switch {
			case poll.Media == "":
				problem = codeNotReady // a board too old to say why
			case p.Family == wire.FamilyPTouch:
				problem = wire.CodeUnsupportedTape
			default:
				problem = wire.CodeUnsupportedMedia
			}
		}
	}
	now := s.now()
	s.mu.Lock()
	was, wasWiFi := p.problem, p.wifi
	p.lastPoll, p.media, p.problem, p.wifi = now, media, problem, poll.WiFi
	s.mu.Unlock()
	log := s.log.With("printer", p.Name)
	if poll.WiFi != wasWiFi {
		log.Info("board's Wi-Fi", "wifi", poll.WiFi, "was", wasWiFi)
	}
	switch {
	case problem != was && problem != "":
		log.Warn("printer can't print", "problem", problem, "media", poll.Media)
	case problem != was:
		log.Info("printer ready again", "was", was)
	}
	job, err := s.store.Claim(ctx, p.Name, now)
	if job == nil || err != nil {
		return nil, err
	}
	if !poll.Draws {
		drawn, failed := s.draw(job, p.Family, media, problem)
		if failed != nil {
			// Nothing was sent, so it's over: jobs are never requeued.
			log.Warn("job not sent", "job", job.ID, "kind", job.Kind, "code", failed.Code, "err", failed.Error)
			return nil, s.store.Finish(ctx, p.Name, job.ID, *failed, s.now())
		}
		job = drawn
	}
	log.Info("job sent", "job", job.ID, "kind", job.Kind)
	return job, nil
}

// draw makes a claimed job the raster its board prints on the loaded media, or
// says why it can't: the media may have changed since it was submitted.
func (s *Service) draw(job *wire.Job, family string, media wire.Media, problem string) (*wire.Job, *wire.Result) {
	fail := func(code, format string, args ...any) (*wire.Job, *wire.Result) {
		r := wire.Failed(code, fmt.Sprintf(format, args...))
		r.LoadedMedia, r.LoadedTapeMM = media.ID, media.TapeMM
		return nil, &r
	}
	switch {
	case problem != "":
		return fail(problem, "%s", fix(family, problem))
	case job.Media != "" && job.Media != media.ID:
		return fail(wire.CodeWrongMedia, "the job is for %s but %s is loaded", job.Media, media.ID)
	case job.Kind == wire.KindImage && job.TapeMM != media.TapeMM:
		return fail(wire.CodeTapeMismatch, "image is for %d mm tape but %d mm is loaded", job.TapeMM, media.TapeMM)
	}
	raster, err := label.Draw(*job, media)
	if te, ok := errors.AsType[*label.TextError](err); ok {
		return fail(te.Code, "%s", te.Msg)
	}
	if err != nil {
		return fail(wire.CodeInternal, "drawing the label: %v", err)
	}
	return &wire.Job{ID: job.ID, Kind: job.Kind, Raster: raster}, nil
}

// Report records the result of a job the printer's board took.
func (s *Service) Report(ctx context.Context, printer string, id int64, r wire.Result) error {
	err := s.store.Finish(ctx, printer, id, r, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return refuse(http.StatusNotFound, "not_found", "no job %d", id)
	case errors.Is(err, store.ErrNotSent):
		return refuse(http.StatusConflict, "not_sent", "job %d isn't waiting for a result", id)
	case err != nil:
		return err
	}
	s.log.Info("job finished", "job", id, "printer", printer, "printed", r.Printed, "code", r.Code)
	return nil
}

// Submit queues a job for who, once it is sure the job's printer can print it
// now. Nothing about the printer is promised beyond that: a job is never retried.
func (s *Service) Submit(ctx context.Context, who string, req wire.Job) (store.Job, error) {
	p, err := s.printer(req.Printer)
	if err != nil {
		return store.Job{}, err
	}
	// Only what the kind uses is kept.
	job := wire.Job{Printer: p.Name, Kind: req.Kind, Media: req.Media}
	var size image.Config
	switch req.Kind {
	case wire.KindBarcode:
		if msg := wire.ValidateText(req.Text); msg != "" {
			return store.Job{}, refuse(http.StatusBadRequest, "bad_text", "%s", msg)
		}
		job.Text = req.Text
	case wire.KindText:
		// Checked below, once the loaded media is known.
		job.Lines, job.Size, job.Align = req.Lines, req.Size, req.Align
	case wire.KindImage:
		if len(req.Image) > wire.MaxImageBytes {
			return store.Job{}, refuse(http.StatusRequestEntityTooLarge, "too_large", "image is over %d bytes", wire.MaxImageBytes)
		}
		if size, err = png.DecodeConfig(bytes.NewReader(req.Image)); err != nil {
			return store.Job{}, refuse(http.StatusBadRequest, "bad_image", "image must be a PNG")
		}
		if p.Family == wire.FamilyPTouch && req.TapeMM <= 0 {
			return store.Job{}, refuse(http.StatusBadRequest, "bad_request", "tape_mm is required for an image")
		}
		job.Image, job.TapeMM = req.Image, req.TapeMM
	default:
		return store.Job{}, refuse(http.StatusBadRequest, "bad_request", `kind must be "barcode", "text" or "image"`)
	}
	if job.Orientation, err = orientation(p.Family, req); err != nil {
		return store.Job{}, err
	}
	if p.Family != wire.FamilyPTouch && req.TapeMM != 0 {
		return store.Job{}, refuse(http.StatusBadRequest, "bad_request", "%s doesn't take tape; leave out tape_mm", p.Name)
	}

	s.mu.Lock()
	st := s.status(p)
	s.mu.Unlock()
	if !st.Online {
		return store.Job{}, refuse(http.StatusServiceUnavailable, "printer_offline",
			"%s hasn't checked in for over %v; check its power and Wi-Fi", p.Name, OfflineAfter)
	}
	if st.Problem != "" {
		return store.Job{}, refuse(http.StatusServiceUnavailable, st.Problem, "%s", st.Fix)
	}
	if job.Media != "" && job.Media != st.Media {
		return store.Job{}, refuse(http.StatusConflict, wire.CodeWrongMedia,
			"the job is for %s but %s has %s loaded", job.Media, p.Name, st.Media)
	}
	if p.Family != wire.FamilyPTouch {
		job.Media = st.Media // so the board can refuse it if the labels are changed meanwhile
	}
	switch job.Kind {
	case wire.KindText:
		t := label.TextLabel{Lines: job.Lines, Size: job.Size, Align: job.Align}
		if p.Family == wire.FamilyPTouch {
			t, err = label.CheckText(t, st.TapeMM)
		} else {
			t, err = label.CheckDieCutText(t)
		}
		if err != nil {
			te := err.(*label.TextError)
			return store.Job{}, refuse(http.StatusBadRequest, te.Code, "%s", te.Msg)
		}
		job.Lines, job.Size, job.Align = t.Lines, t.Size, t.Align
	case wire.KindImage:
		if job.TapeMM != st.TapeMM {
			return store.Job{}, refuse(http.StatusConflict, "tape_mismatch",
				"image is for %d mm tape but %d mm is loaded", job.TapeMM, st.TapeMM)
		}
		if st.WidthDots != 0 && (size.Width != st.WidthDots || size.Height != st.HeightDots) {
			return store.Job{}, refuse(http.StatusUnprocessableEntity, "wrong_size",
				"image is %dx%d dots; %s needs %dx%d", size.Width, size.Height, st.Media, st.WidthDots, st.HeightDots)
		}
		if size.Height != st.HeightDots {
			return store.Job{}, refuse(http.StatusUnprocessableEntity, "wrong_height",
				"image is %d dots high; %d mm tape needs %d", size.Height, st.TapeMM, st.HeightDots)
		}
	}

	// Drawing it is how to know it fits. It is drawn again for the board.
	media, ok := wire.FindMedia(p.Family, st.Media)
	if !ok {
		return store.Job{}, fmt.Errorf("%s has unknown media %q", p.Name, st.Media)
	}
	if _, err := label.Draw(job, media); err != nil {
		if te, ok := errors.AsType[*label.TextError](err); ok {
			return store.Job{}, refuse(http.StatusBadRequest, te.Code, "%s", te.Msg)
		}
		return store.Job{}, err
	}

	j, err := s.store.Add(ctx, job, who, s.now())
	if err == nil {
		s.log.Info("job queued", "job", j.ID, "printer", p.Name, "kind", j.Kind, "submitter", who)
	}
	return j, err
}

// orientation checks a job's orientation, which only text and barcodes on a
// LabelWriter have, and gives it its default.
func orientation(family string, req wire.Job) (string, error) {
	if family == wire.FamilyPTouch || req.Kind == wire.KindImage {
		if req.Orientation != "" {
			return "", refuse(http.StatusBadRequest, "bad_request", "orientation is only for text and barcodes on a LabelWriter")
		}
		return "", nil
	}
	switch req.Orientation {
	case "":
		return wire.OrientPortrait, nil
	case wire.OrientPortrait, wire.OrientLandscape:
		return req.Orientation, nil
	}
	return "", refuse(http.StatusBadRequest, "bad_request", `orientation must be "portrait" or "landscape"`)
}

func (s *Service) Get(ctx context.Context, id int64) (store.Job, error) {
	j, err := s.store.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return j, refuse(http.StatusNotFound, "not_found", "no job %d", id)
	}
	return j, err
}

// List returns the newest jobs. limit <= 0 means the default.
func (s *Service) List(ctx context.Context, limit int) ([]store.Job, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	return s.store.List(ctx, min(limit, maxListLimit))
}

// Prune deletes jobs older than Retention, then again every day until ctx ends.
func (s *Service) Prune(ctx context.Context) {
	for {
		if n, err := s.store.Prune(ctx, s.now().Add(-Retention)); err != nil {
			s.log.Error("pruning old jobs", "err", err)
		} else if n > 0 {
			s.log.Info("pruned old jobs", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
