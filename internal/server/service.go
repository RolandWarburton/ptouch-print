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

	"github.com/RolandWarburton/ptouch-print-server/internal/ptouch"
	"github.com/RolandWarburton/ptouch-print-server/internal/store"
	"github.com/RolandWarburton/ptouch-print-server/internal/wire"
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

	mu       sync.Mutex
	lastPoll time.Time
	tapeMM   int // what the board last reported; 0 = no printer or unsupported tape
}

func NewService(st *store.Store, log *slog.Logger) *Service {
	return &Service{store: st, log: log, now: time.Now}
}

// Next records the board's poll and hands it the oldest queued job, if any.
func (s *Service) Next(ctx context.Context, tapeMM int) (*wire.Job, error) {
	now := s.now()
	s.mu.Lock()
	s.lastPoll, s.tapeMM = now, tapeMM
	s.mu.Unlock()
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
		return store.Job{}, refuse(http.StatusBadRequest, "bad_request", `kind must be "barcode" or "image"`)
	}

	st := s.Status()
	if !st.Online {
		return store.Job{}, refuse(http.StatusServiceUnavailable, "printer_offline",
			"the printer hasn't checked in for over %v; check its power and Wi-Fi", OfflineAfter)
	}
	if st.TapeMM == 0 {
		return store.Job{}, refuse(http.StatusServiceUnavailable, "no_tape",
			"the board can't see the printer or a supported tape (12 or 24 mm)")
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
}

func (s *Service) Status() BoardStatus {
	s.mu.Lock()
	last, tape := s.lastPoll.UTC(), s.tapeMM
	s.mu.Unlock()
	if last.IsZero() {
		return BoardStatus{}
	}
	st := BoardStatus{Online: s.now().Sub(last) <= OfflineAfter, LastPoll: &last}
	if st.Online {
		st.TapeMM = tape
		if t, ok := ptouch.Tape(tape); ok {
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
