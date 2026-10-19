package ptouch

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"time"

	"github.com/RolandWarburton/ptouch-print-server/internal/wire"
)

// Device is an open connection to the printer, normally /dev/usb/lpN.
type Device interface {
	io.ReadWriteCloser
}

// Opener finds and opens the printer. It is called for every request so the
// printer can be unplugged and plugged back in at any time.
type Opener func() (Device, error)

type Config struct {
	BusyTimeout   time.Duration // how long to wait for another job to finish
	StatusTimeout time.Duration // how long to wait for a status reply
	PrintTimeout  time.Duration // how long to wait for "printing completed"
	PollInterval  time.Duration // delay between reads while the printer is quiet
	MaxLabelDots  int           // longest label accepted, in dots
}

var (
	ErrBusy        = errors.New("printer busy with another job")
	ErrNoReply     = errors.New("printer didn't answer the status request")
	ErrUnconfirmed = errors.New("sent the label but the printer never confirmed it printed")
	ErrEmptyLabel  = errors.New("label is empty")
)

type UnsupportedTapeError struct{ MM int }

func (e *UnsupportedTapeError) Error() string {
	return fmt.Sprintf("unsupported tape width %d mm", e.MM)
}

type TapeMismatchError struct{ Want, Loaded int }

func (e *TapeMismatchError) Error() string {
	return fmt.Sprintf("requested %d mm tape but %d mm is loaded", e.Want, e.Loaded)
}

type ImageHeightError struct{ Got, Want int }

func (e *ImageHeightError) Error() string {
	return fmt.Sprintf("image is %d dots high, the loaded tape needs exactly %d", e.Got, e.Want)
}

type LabelTooLongError struct{ Got, Max int }

func (e *LabelTooLongError) Error() string {
	return fmt.Sprintf("label is %d dots long, the maximum is %d", e.Got, e.Max)
}

// RenderFunc draws the label for the loaded tape.
type RenderFunc func(wire.TapeSpec) (image.Image, error)

type Result struct {
	Tape       wire.TapeSpec
	LengthDots int
}

// Printer talks to one PT-P700, one job at a time.
type Printer struct {
	open Opener
	cfg  Config
	slot chan struct{}
}

func New(open Opener, cfg Config) *Printer {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 50 * time.Millisecond
	}
	return &Printer{open: open, cfg: cfg, slot: make(chan struct{}, 1)}
}

func (p *Printer) acquire(ctx context.Context) (release func(), err error) {
	t := time.NewTimer(p.cfg.BusyTimeout)
	defer t.Stop()
	select {
	case p.slot <- struct{}{}:
		return func() { <-p.slot }, nil
	case <-t.C:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Status asks the printer what is loaded and whether it has any errors.
func (p *Printer) Status(ctx context.Context) (Status, error) {
	release, err := p.acquire(ctx)
	if err != nil {
		return Status{}, err
	}
	defer release()
	dev, err := p.open()
	if err != nil {
		return Status{}, err
	}
	defer dev.Close()
	return p.queryStatus(dev)
}

// Print checks the printer is ready, renders the label for the loaded tape
// and prints it. wantMM is the tape the caller expects, or 0 for any.
func (p *Printer) Print(ctx context.Context, wantMM int, render RenderFunc) (Result, error) {
	release, err := p.acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	defer release()
	dev, err := p.open()
	if err != nil {
		return Result{}, err
	}
	defer dev.Close()

	st, err := p.queryStatus(dev)
	if err != nil {
		return Result{}, err
	}
	if len(st.Errors) > 0 {
		return Result{}, &PrinterError{Errors: st.Errors, E1: st.e1, E2: st.e2}
	}
	t, ok := wire.Tape(st.TapeMM)
	if !ok {
		return Result{}, &UnsupportedTapeError{MM: st.TapeMM}
	}
	if wantMM != 0 && wantMM != st.TapeMM {
		return Result{}, &TapeMismatchError{Want: wantMM, Loaded: st.TapeMM}
	}

	img, err := render(t)
	if err != nil {
		return Result{}, err
	}
	b := img.Bounds()
	switch {
	case b.Dy() != t.Dots:
		return Result{}, &ImageHeightError{Got: b.Dy(), Want: t.Dots}
	case b.Dx() == 0:
		return Result{}, ErrEmptyLabel
	case p.cfg.MaxLabelDots > 0 && b.Dx() > p.cfg.MaxLabelDots:
		return Result{}, &LabelTooLongError{Got: b.Dx(), Max: p.cfg.MaxLabelDots}
	}
	// Last chance to back out: once the job is sent, see it through even if
	// the client has gone, so the result is logged.
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if _, err := dev.Write(encodeJob(img, t, st.MediaType)); err != nil {
		return Result{}, fmt.Errorf("sending label: %w", err)
	}
	if err := p.waitForPrint(dev); err != nil {
		return Result{}, err
	}
	return Result{Tape: t, LengthDots: b.Dx()}, nil
}

func (p *Printer) queryStatus(dev Device) (Status, error) {
	if _, err := dev.Write(statusRequest()); err != nil {
		return Status{}, fmt.Errorf("requesting status: %w", err)
	}
	buf := make([]byte, statusLen)
	if err := readFull(dev, buf, time.Now().Add(p.cfg.StatusTimeout), p.cfg.PollInterval); err != nil {
		if errors.Is(err, errTimeout) {
			return Status{}, ErrNoReply
		}
		return Status{}, fmt.Errorf("reading printer status: %w", err)
	}
	return parseStatus(buf)
}

// waitForPrint watches the status messages the printer sends while it works
// and reports whether the label printed or the printer rejected it.
func (p *Printer) waitForPrint(dev Device) error {
	deadline := time.Now().Add(p.cfg.PrintTimeout)
	for {
		buf := make([]byte, statusLen)
		if err := readFull(dev, buf, deadline, p.cfg.PollInterval); err != nil {
			if errors.Is(err, errTimeout) {
				return ErrUnconfirmed
			}
			return fmt.Errorf("%w (%v)", ErrUnconfirmed, err)
		}
		st, err := parseStatus(buf)
		if err != nil {
			continue
		}
		switch st.Type {
		case statusTypeCompleted:
			return nil
		case statusTypeError:
			return &PrinterError{Errors: st.Errors, AfterSend: true, E1: st.e1, E2: st.e2}
		}
	}
}

var errTimeout = errors.New("timed out")

// readFull fills buf with the printer's reply. usblp returns EOF rather than
// blocking while the printer hasn't answered yet, so keep polling.
func readFull(r io.Reader, buf []byte, deadline time.Time, poll time.Duration) error {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n < len(buf) {
			if !time.Now().Before(deadline) {
				return errTimeout
			}
			time.Sleep(poll)
		}
	}
	return nil
}
