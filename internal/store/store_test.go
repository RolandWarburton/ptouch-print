package store

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var t0 = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

func TestLifecycle(t *testing.T) {
	s, ctx := open(t), t.Context()
	a, err := s.Add(ctx, wire.Job{Kind: wire.KindBarcode, Text: "A"}, "me@example.com", t0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Add(ctx, wire.Job{Kind: wire.KindImage, TapeMM: 24, Image: []byte("png")}, "token:ci", t0)
	if a.Status != Queued || a.Submitter != "me@example.com" || !a.CreatedAt.Equal(t0) {
		t.Fatalf("added %+v", a)
	}

	// Oldest first.
	got, err := s.Claim(ctx, t0.Add(time.Second))
	if err != nil || got == nil || got.ID != a.ID || got.Text != "A" {
		t.Fatalf("first claim %+v %v", got, err)
	}
	got, _ = s.Claim(ctx, t0)
	if got == nil || got.ID != b.ID || string(got.Image) != "png" || got.TapeMM != 24 {
		t.Fatalf("second claim %+v", got)
	}
	if got, err := s.Claim(ctx, t0); got != nil || err != nil {
		t.Fatalf("empty queue: %+v %v", got, err)
	}

	if err := s.Finish(ctx, a.ID, wire.Result{Printed: true, TapeMM: 24}, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, b.ID, wire.Failed("unconfirmed", "no confirmation"), t0); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.Get(ctx, a.ID); j.Status != Done || j.Result == nil || !j.Result.Printed || j.SentAt == nil || j.FinishedAt == nil {
		t.Errorf("finished job %+v", j)
	}
	if j, _ := s.Get(ctx, b.ID); j.Status != Failed || j.Result.Code != "unconfirmed" {
		t.Errorf("failed job %+v", j)
	}

	// Terminal states don't move.
	if err := s.Finish(ctx, a.ID, wire.Failed("x", "x"), t0); !errors.Is(err, ErrNotSent) {
		t.Errorf("finishing twice: %v", err)
	}
	if err := s.Finish(ctx, 999, wire.Result{}, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("finishing a missing job: %v", err)
	}
	if _, err := s.Get(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}
}

func TestTextJob(t *testing.T) {
	s, ctx := open(t), t.Context()
	job := wire.Job{Kind: wire.KindText, Lines: []string{"Shelf A", "Bin 3"}, Size: "small", Align: "right"}
	added, err := s.Add(ctx, job, "x", t0)
	if err != nil {
		t.Fatal(err)
	}
	if added.Text != "" || !slices.Equal(added.Lines, job.Lines) || added.Size != "small" || added.Align != "right" {
		t.Errorf("added %+v", added)
	}
	got, err := s.Claim(ctx, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != wire.KindText || got.Text != "" || !slices.Equal(got.Lines, job.Lines) || got.Size != "small" || got.Align != "right" {
		t.Errorf("claimed %+v", got)
	}
}

func TestFinishNeedsSent(t *testing.T) {
	s, ctx := open(t), t.Context()
	j, _ := s.Add(ctx, wire.Job{Kind: wire.KindBarcode, Text: "A"}, "x", t0)
	if err := s.Finish(ctx, j.ID, wire.Result{Printed: true}, t0); !errors.Is(err, ErrNotSent) {
		t.Errorf("finishing a queued job: %v", err)
	}
}

func TestListAndPrune(t *testing.T) {
	s, ctx := open(t), t.Context()
	for i := range 3 {
		s.Add(ctx, wire.Job{Kind: wire.KindBarcode, Text: "A"}, "x", t0.AddDate(0, 0, i))
	}
	jobs, err := s.List(ctx, 2)
	if err != nil || len(jobs) != 2 || jobs[0].ID != 3 {
		t.Fatalf("list %+v %v", jobs, err)
	}
	n, err := s.Prune(ctx, t0.AddDate(0, 0, 2))
	if err != nil || n != 2 {
		t.Fatalf("pruned %d %v", n, err)
	}
	if jobs, _ := s.List(ctx, 10); len(jobs) != 1 || jobs[0].ID != 3 {
		t.Errorf("after prune %+v", jobs)
	}
}
