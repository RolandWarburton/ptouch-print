package board

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RolandWarburton/ptouch-print-server/internal/ptouch"
	"github.com/RolandWarburton/ptouch-print-server/internal/wire"
)

var tape24, _ = wire.Tape(24)

// fakePrinter has 24 mm tape loaded unless tapeMM says otherwise. Print
// renders the label and returns printErr.
type fakePrinter struct {
	statusErr  error
	statusErrs []string // error bits the printer reports
	tapeMM     int
	printErr   error
	gotWantMM  int
	rendered   image.Image
}

func (f *fakePrinter) Status(context.Context) (ptouch.Status, error) {
	tape := f.tapeMM
	if tape == 0 {
		tape = 24
	}
	return ptouch.Status{TapeMM: tape, MediaType: 1, Errors: f.statusErrs}, f.statusErr
}

func (f *fakePrinter) Print(_ context.Context, wantMM int, render ptouch.RenderFunc) (ptouch.Result, error) {
	f.gotWantMM = wantMM
	if f.printErr != nil {
		return ptouch.Result{}, f.printErr
	}
	img, err := render(tape24)
	if err != nil {
		return ptouch.Result{}, err
	}
	f.rendered = img
	return ptouch.Result{Tape: tape24, LengthDots: img.Bounds().Dx()}, nil
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRunBarcode(t *testing.T) {
	p := &fakePrinter{}
	res := Run(t.Context(), p, wire.Job{ID: 1, Kind: wire.KindBarcode, Text: "F2LXK0ABHG7F"})
	if !res.Printed || res.TapeMM != 24 || res.Code != "" {
		t.Fatalf("result %+v", res)
	}
	if p.gotWantMM != 0 {
		t.Errorf("barcode asked for %d mm, want any tape (0)", p.gotWantMM)
	}
	if p.rendered.Bounds().Dy() != 128 {
		t.Errorf("rendered %d dots high, want 128", p.rendered.Bounds().Dy())
	}
}

func TestRunImage(t *testing.T) {
	p := &fakePrinter{}
	res := Run(t.Context(), p, wire.Job{Kind: wire.KindImage, TapeMM: 24, Image: pngBytes(t, 300, 128)})
	if !res.Printed || res.LengthDots != 300 || p.gotWantMM != 24 {
		t.Errorf("result %+v, asked for %d mm", res, p.gotWantMM)
	}
}

func TestRunBadJobs(t *testing.T) {
	for _, tt := range []struct {
		name string
		job  wire.Job
		code string
	}{
		{"empty text", wire.Job{Kind: wire.KindBarcode}, "bad_text"},
		{"non-ASCII", wire.Job{Kind: wire.KindBarcode, Text: "café"}, "bad_text"},
		{"long text", wire.Job{Kind: wire.KindBarcode, Text: strings.Repeat("A", wire.MaxTextLen+1)}, "bad_text"},
		{"unknown kind", wire.Job{Kind: "qr", Text: "A"}, "bad_request"},
		{"image without tape", wire.Job{Kind: wire.KindImage, Image: pngBytes(t, 10, 128)}, "bad_request"},
		{"not a PNG", wire.Job{Kind: wire.KindImage, TapeMM: 24, Image: []byte("GIF89a")}, "bad_image"},
		{"too tall", wire.Job{Kind: wire.KindImage, TapeMM: 24, Image: pngBytes(t, 10, 129)}, "bad_image"},
		{"too wide", wire.Job{Kind: wire.KindImage, TapeMM: 24, Image: pngBytes(t, maxImageWidth+1, 1)}, "bad_image"},
	} {
		p := &fakePrinter{}
		if res := Run(t.Context(), p, tt.job); res.Code != tt.code || res.Printed || p.rendered != nil {
			t.Errorf("%s: %+v, want %s and nothing printed", tt.name, res, tt.code)
		}
	}
}

func TestRunPrinterErrors(t *testing.T) {
	for _, tt := range []struct {
		err  error
		code string
	}{
		{ptouch.ErrBusy, "busy"},
		{ptouch.ErrUnconfirmed, "unconfirmed"},
		{ptouch.ErrNotConnected, "not_connected"},
		{ptouch.ErrEditorLite, "editor_lite"},
		{ptouch.ErrNoReply, "no_reply"},
		{&ptouch.PrinterError{Errors: []string{"cover open"}}, "printer_error"},
		{&ptouch.PrinterError{Errors: []string{"tape ran out"}, AfterSend: true}, "print_failed"},
		{&ptouch.UnsupportedTapeError{MM: 18}, "unsupported_tape"},
		{&ptouch.TapeMismatchError{Want: 12, Loaded: 24}, "tape_mismatch"},
		{&ptouch.ImageHeightError{Got: 70, Want: 128}, "wrong_height"},
		{&ptouch.LabelTooLongError{Got: 5000, Max: 3600}, "too_long"},
		{ptouch.ErrEmptyLabel, "empty_label"},
		{ptouch.ErrPermission, "permission"},
		{context.Canceled, "canceled"},
		{errors.New("something else"), "internal"},
	} {
		res := Run(t.Context(), &fakePrinter{printErr: tt.err}, wire.Job{Kind: wire.KindBarcode, Text: "ABC"})
		if res.Printed || res.Code != tt.code {
			t.Errorf("%v: code %q, want %s", tt.err, res.Code, tt.code)
		}
	}
	res := Run(t.Context(), &fakePrinter{printErr: ptouch.ErrUnconfirmed}, wire.Job{Kind: wire.KindBarcode, Text: "ABC"})
	if !res.MayHavePrinted {
		t.Errorf("unconfirmed print should say it may have printed: %+v", res)
	}
	res = Run(t.Context(), &fakePrinter{printErr: &ptouch.PrinterError{Errors: []string{"cover open"}}},
		wire.Job{Kind: wire.KindBarcode, Text: "ABC"})
	if len(res.PrinterErrors) != 1 || res.PrinterErrors[0] != "cover open" {
		t.Errorf("printer errors not passed on: %+v", res)
	}
}

// fakeServer hands out the jobs in queue, one per poll, and records what the
// board sent.
type fakeServer struct {
	mu       sync.Mutex
	queue    []wire.Job
	tapes    []string
	problems []string
	results  map[int64]wire.Result
	auth     []string
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == wire.NextPath:
		s.tapes = append(s.tapes, r.URL.Query().Get("tape_mm"))
		s.problems = append(s.problems, r.URL.Query().Get(wire.ProblemParam))
		if len(s.queue) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		json.NewEncoder(w).Encode(s.queue[0])
		s.queue = s.queue[1:]
	case r.Method == http.MethodPost && r.URL.Path == wire.ResultPath(7):
		var res wire.Result
		json.NewDecoder(r.Body).Decode(&res)
		s.results[7] = res
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "no", http.StatusNotFound)
	}
}

func newPoller(t *testing.T, s http.Handler, p Printer) *Poller {
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return &Poller{
		ServerURL: srv.URL + "/", Token: "tok", Interval: time.Hour, Printer: p, Client: srv.Client(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestPoll(t *testing.T) {
	s := &fakeServer{queue: []wire.Job{{ID: 7, Kind: wire.KindBarcode, Text: "ABC"}}, results: map[int64]wire.Result{}}
	p := newPoller(t, s, &fakePrinter{})
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if res, ok := s.results[7]; !ok || !res.Printed {
		t.Errorf("reported %+v (%v)", res, ok)
	}
	if err := p.Poll(t.Context()); err != nil {
		t.Fatalf("empty queue: %v", err)
	}
	if strings.Join(s.tapes, ",") != "24,24" || s.auth[0] != "Bearer tok" {
		t.Errorf("tapes %v, auth %v", s.tapes, s.auth)
	}
}

func TestPollReportsFailures(t *testing.T) {
	s := &fakeServer{queue: []wire.Job{{ID: 7, Kind: wire.KindBarcode, Text: "ABC"}}, results: map[int64]wire.Result{}}
	if err := newPoller(t, s, &fakePrinter{printErr: ptouch.ErrUnconfirmed}).Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if res := s.results[7]; res.Printed || res.Code != "unconfirmed" || !res.MayHavePrinted {
		t.Errorf("reported %+v", res)
	}
}

func TestPollReportsProblems(t *testing.T) {
	for _, tt := range []struct {
		name    string
		printer *fakePrinter
		problem string
	}{
		{"ready", &fakePrinter{}, ""},
		{"no printer", &fakePrinter{statusErr: ptouch.ErrNotConnected}, "not_connected"},
		{"Editor Lite", &fakePrinter{statusErr: ptouch.ErrEditorLite}, "editor_lite"},
		{"no reply", &fakePrinter{statusErr: ptouch.ErrNoReply}, "no_reply"},
		{"cover open", &fakePrinter{statusErrs: []string{"cover open"}}, "printer_error"},
		{"18 mm tape", &fakePrinter{tapeMM: 18}, "unsupported_tape"},
	} {
		s := &fakeServer{results: map[int64]wire.Result{}}
		if err := newPoller(t, s, tt.printer).Poll(t.Context()); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		wantTape := "0"
		if tt.problem == "" {
			wantTape = "24"
		}
		if s.tapes[0] != wantTape || s.problems[0] != tt.problem {
			t.Errorf("%s: tape_mm=%s problem=%q, want %s %q", tt.name, s.tapes[0], s.problems[0], wantTape, tt.problem)
		}
	}
}

func TestPollServerError(t *testing.T) {
	p := newPoller(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing or wrong bearer token", http.StatusUnauthorized)
	}), &fakePrinter{})
	if err := p.Poll(t.Context()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err %v", err)
	}
}
