// Package server is the command server: a job queue in SQLite with three ways
// in — the board's poll API, the REST submit API and MCP — all calling Service.
package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	// OfflineAfter is how long since the board's last poll before submits are
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
	store *store.Store
	log   *slog.Logger
	now   func() time.Time

	// What the board's last poll said: the tape, or 0 and why it can't print,
	// and the Wi-Fi network it's on.
	mu       sync.Mutex
	lastPoll time.Time
	tapeMM   int
	problem  string
	wifi     string
}

// problems says how to fix each reason the board gives for not printing.
var problems = map[string]string{
	wire.CodeEditorLite:      "The printer is in Editor Lite mode. Hold its Editor Lite button until the light goes off.",
	wire.CodeNotConnected:    "The board can't find the printer. Check it's switched on and plugged into the board's data USB port.",
	wire.CodePrinterError:    "The printer reports an error, such as its cover open or no tape. Check the printer.",
	wire.CodeUnsupportedTape: "The loaded tape isn't supported. Load 12 or 24 mm tape.",
	wire.CodePermission:      "The board isn't allowed to open the printer. That's a setup problem on the board (its udev rule or lp group).",
	wire.CodeNoReply:         "The printer didn't answer the board. Try switching it off and on.",
	wire.CodeBusy:            "The printer is busy with something else.",
	"printer_not_ready":      "The board says the printer can't print but not why. Check the printer.",
}

// notReady is the refusal for a board that polls without a ready printer.
func notReady(problem string) *Error {
	if problem == "" {
		problem = "printer_not_ready" // a board too old to say why
	}
	msg, ok := problems[problem]
	if !ok {
		msg = "The printer can't print right now (" + problem + ")."
	}
	return &Error{Status: http.StatusServiceUnavailable, Code: problem, Msg: msg}
}

func NewService(st *store.Store, log *slog.Logger) *Service {
	return &Service{store: st, log: log, now: time.Now}
}

// Next records the board's poll and hands it the oldest queued job, if any,
// even while the printer has a problem: that job fails now rather than
// printing later, unexpectedly.
func (s *Service) Next(ctx context.Context, tapeMM int, problem, wifi string) (*wire.Job, error) {
	now := s.now()
	s.mu.Lock()
	was, wasWiFi := s.problem, s.wifi
	s.lastPoll, s.tapeMM, s.problem, s.wifi = now, tapeMM, problem, wifi
	s.mu.Unlock()
	if wifi != wasWiFi {
		s.log.Info("board's Wi-Fi", "wifi", wifi, "was", wasWiFi)
	}
	switch {
	case problem != was && problem != "":
		s.log.Warn("printer can't print", "problem", problem)
	case problem != was:
		s.log.Info("printer ready again", "was", was)
	}
	job, err := s.store.Claim(ctx, now)
	if job != nil {
		s.log.Info("job sent", "job", job.ID, "kind", job.Kind)
	}
	return job, err
}

// Report records the result of a job the board took.
func (s *Service) Report(ctx context.Context, id int64, r wire.Result) error {
	err := s.store.Finish(ctx, id, r, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return refuse(http.StatusNotFound, "not_found", "no job %d", id)
	case errors.Is(err, store.ErrNotSent):
		return refuse(http.StatusConflict, "not_sent", "job %d isn't waiting for a result", id)
	case err != nil:
		return err
	}
	s.log.Info("job finished", "job", id, "printed", r.Printed, "code", r.Code)
	return nil
}

// Submit queues a job for who, once it is sure the board can print it now.
// Nothing about the printer is promised beyond that: a job is never retried.
func (s *Service) Submit(ctx context.Context, who string, job wire.Job) (store.Job, error) {
	var height int
	switch job.Kind {
	case wire.KindBarcode:
		if msg := wire.ValidateText(job.Text); msg != "" {
			return store.Job{}, refuse(http.StatusBadRequest, "bad_text", "%s", msg)
		}
		job = wire.Job{Kind: job.Kind, Text: job.Text}
	case wire.KindText:
		// Checked below, once the loaded tape is known.
		job = wire.Job{Kind: job.Kind, Lines: job.Lines, Size: job.Size, Align: job.Align}
	case wire.KindImage:
		if len(job.Image) > wire.MaxImageBytes {
			return store.Job{}, refuse(http.StatusRequestEntityTooLarge, "too_large", "image is over %d bytes", wire.MaxImageBytes)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(job.Image))
		if err != nil {
			return store.Job{}, refuse(http.StatusBadRequest, "bad_image", "image must be a PNG")
		}
		if job.TapeMM <= 0 {
			return store.Job{}, refuse(http.StatusBadRequest, "bad_request", "tape_mm is required for an image")
		}
		height = cfg.Height
		job = wire.Job{Kind: job.Kind, TapeMM: job.TapeMM, Image: job.Image}
	default:
		return store.Job{}, refuse(http.StatusBadRequest, "bad_request", `kind must be "barcode", "text" or "image"`)
	}

	st := s.Status()
	if !st.Online {
		return store.Job{}, refuse(http.StatusServiceUnavailable, "printer_offline",
			"the printer hasn't checked in for over %v; check its power and Wi-Fi", OfflineAfter)
	}
	if st.TapeMM == 0 {
		return store.Job{}, notReady(st.Problem)
	}
	if job.Kind == wire.KindText {
		// What needs drawing (glyphs, length) is checked on the board.
		t, err := label.CheckText(label.TextLabel{Lines: job.Lines, Size: job.Size, Align: job.Align}, st.TapeMM)
		if err != nil {
			te := err.(*label.TextError)
			return store.Job{}, refuse(http.StatusBadRequest, te.Code, "%s", te.Msg)
		}
		job.Lines, job.Size, job.Align = t.Lines, t.Size, t.Align
	}
	if job.Kind == wire.KindImage {
		if job.TapeMM != st.TapeMM {
			return store.Job{}, refuse(http.StatusConflict, "tape_mismatch",
				"image is for %d mm tape but %d mm is loaded", job.TapeMM, st.TapeMM)
		}
		if height != st.HeightDots {
			return store.Job{}, refuse(http.StatusUnprocessableEntity, "wrong_height",
				"image is %d dots high; %d mm tape needs %d", height, st.TapeMM, st.HeightDots)
		}
	}

	j, err := s.store.Add(ctx, job, who, s.now())
	if err == nil {
		s.log.Info("job queued", "job", j.ID, "kind", j.Kind, "submitter", who)
	}
	return j, err
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

// BoardStatus is what the server knows of the board from its polls.
type BoardStatus struct {
	Online     bool       `json:"online"`
	LastPoll   *time.Time `json:"last_poll,omitempty"`
	TapeMM     int        `json:"tape_mm"`
	HeightDots int        `json:"height_dots,omitempty"` // the image height the tape needs
	Problem    string     `json:"problem,omitempty"`     // why it can't print, e.g. editor_lite
	Fix        string     `json:"fix,omitempty"`         // how to fix Problem, for people
	WiFi       string     `json:"wifi,omitempty"`        // the board's Wi-Fi network at LastPoll
}

func (s *Service) Status() BoardStatus {
	s.mu.Lock()
	last, tape, problem, wifi := s.lastPoll.UTC(), s.tapeMM, s.problem, s.wifi
	s.mu.Unlock()
	if last.IsZero() {
		return BoardStatus{}
	}
	// The Wi-Fi network is kept while offline: where the board was is a clue to why.
	st := BoardStatus{Online: s.now().Sub(last) <= OfflineAfter, LastPoll: &last, WiFi: wifi}
	if st.Online {
		st.TapeMM, st.Problem = tape, problem
		if problem != "" {
			st.Fix = notReady(problem).Msg
		}
		if t, ok := wire.Tape(tape); ok {
			st.HeightDots = t.Dots
		}
	}
	return st
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
