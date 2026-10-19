// Package board is the SBC side: it polls the command server for jobs, prints
// them and reports the results.
package board

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"

	"github.com/RolandWarburton/ptouch-print-server/internal/label"
	"github.com/RolandWarburton/ptouch-print-server/internal/ptouch"
	"github.com/RolandWarburton/ptouch-print-server/internal/wire"
)

const maxImageWidth = 20000 // decoding limit; the printer enforces max_label_dots

// Printer is the part of *ptouch.Printer the board uses.
type Printer interface {
	Status(ctx context.Context) (ptouch.Status, error)
	Print(ctx context.Context, wantMM int, render ptouch.RenderFunc) (ptouch.Result, error)
}

// Run prints one job. It never retries: an unconfirmed print comes back as a
// failure with MayHavePrinted set.
func Run(ctx context.Context, p Printer, job wire.Job) wire.Result {
	var (
		wantMM int
		render ptouch.RenderFunc
	)
	switch job.Kind {
	case wire.KindBarcode:
		if msg := wire.ValidateText(job.Text); msg != "" {
			return wire.Failed("bad_text", msg)
		}
		render = func(t ptouch.TapeSpec) (image.Image, error) { return label.Barcode(job.Text, t.Dots) }
	case wire.KindImage:
		if job.TapeMM <= 0 {
			return wire.Failed("bad_request", "image job has no tape_mm")
		}
		img, err := decodePNG(bytes.NewReader(job.Image))
		if err != nil {
			return wire.Failed("bad_image", err.Error())
		}
		wantMM = job.TapeMM
		render = func(ptouch.TapeSpec) (image.Image, error) { return img, nil }
	default:
		return wire.Failed("bad_request", fmt.Sprintf("unknown job kind %q", job.Kind))
	}

	res, err := p.Print(ctx, wantMM, render)
	if err != nil {
		return classify(err)
	}
	return wire.Result{Printed: true, TapeMM: res.Tape.MM, LengthDots: res.LengthDots}
}

// decodePNG checks the dimensions before decoding, so a small file can't
// expand into a huge image.
func decodePNG(f io.ReadSeeker) (image.Image, error) {
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return nil, errors.New("image must be a PNG")
	}
	if cfg.Height > ptouch.HeadPins || cfg.Width > maxImageWidth {
		return nil, fmt.Errorf("image is %dx%d, too large for a label", cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, err := png.Decode(f)
	if err != nil {
		return nil, errors.New("image must be a PNG")
	}
	return img, nil
}

func classify(err error) wire.Result {
	resp := wire.Result{Error: err.Error()}
	if pe, ok := errors.AsType[*ptouch.PrinterError](err); ok {
		resp.PrinterErrors = pe.Errors
		if pe.AfterSend {
			resp.Code = "print_failed"
		} else {
			resp.Code = "printer_error"
		}
		return resp
	}
	if ue, ok := errors.AsType[*ptouch.UnsupportedTapeError](err); ok {
		resp.Code, resp.LoadedTapeMM = "unsupported_tape", ue.MM
		return resp
	}
	if me, ok := errors.AsType[*ptouch.TapeMismatchError](err); ok {
		resp.Code, resp.LoadedTapeMM = "tape_mismatch", me.Loaded
		return resp
	}
	if _, ok := errors.AsType[*ptouch.ImageHeightError](err); ok {
		resp.Code = "wrong_height"
		return resp
	}
	if _, ok := errors.AsType[*ptouch.LabelTooLongError](err); ok {
		resp.Code = "too_long"
		return resp
	}
	switch {
	case errors.Is(err, ptouch.ErrEmptyLabel):
		resp.Code = "empty_label"
	case errors.Is(err, ptouch.ErrNotConnected):
		resp.Code = "not_connected"
	case errors.Is(err, ptouch.ErrEditorLite):
		resp.Code = "editor_lite"
	case errors.Is(err, ptouch.ErrNoReply):
		resp.Code = "no_reply"
	case errors.Is(err, ptouch.ErrBusy):
		resp.Code = "busy"
	case errors.Is(err, ptouch.ErrUnconfirmed):
		// The label may already be on the tape, and jobs are never retried.
		resp.Code, resp.MayHavePrinted = "unconfirmed", true
	case errors.Is(err, context.Canceled):
		resp.Code = "canceled"
	case errors.Is(err, ptouch.ErrPermission):
		resp.Code = "permission"
	default:
		resp.Code = "internal"
	}
	return resp
}
