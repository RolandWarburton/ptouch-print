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

	"github.com/RolandWarburton/ptouch-print/internal/label"
	"github.com/RolandWarburton/ptouch-print/internal/ptouch"
	"github.com/RolandWarburton/ptouch-print/internal/wire"
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
			return wire.Failed(wire.CodeBadText, msg)
		}
		render = func(t wire.TapeSpec) (image.Image, error) { return label.Barcode(job.Text, t.Dots) }
	case wire.KindText:
		text := label.TextLabel{Lines: job.Lines, Size: job.Size, Align: job.Align}
		render = func(t wire.TapeSpec) (image.Image, error) { return label.Text(text, t) }
	case wire.KindImage:
		if job.TapeMM <= 0 {
			return wire.Failed(wire.CodeBadRequest, "image job has no tape_mm")
		}
		img, err := decodePNG(bytes.NewReader(job.Image))
		if err != nil {
			return wire.Failed(wire.CodeBadImage, err.Error())
		}
		wantMM = job.TapeMM
		render = func(wire.TapeSpec) (image.Image, error) { return img, nil }
	default:
		return wire.Failed(wire.CodeBadRequest, fmt.Sprintf("unknown job kind %q", job.Kind))
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
	if te, ok := errors.AsType[*label.TextError](err); ok {
		resp.Code = te.Code
		return resp
	}
	if pe, ok := errors.AsType[*ptouch.PrinterError](err); ok {
		resp.PrinterErrors = pe.Errors
		if pe.AfterSend {
			resp.Code = wire.CodePrintFailed
		} else {
			resp.Code = wire.CodePrinterError
		}
		return resp
	}
	if ue, ok := errors.AsType[*ptouch.UnsupportedTapeError](err); ok {
		resp.Code, resp.LoadedTapeMM = wire.CodeUnsupportedTape, ue.MM
		return resp
	}
	if me, ok := errors.AsType[*ptouch.TapeMismatchError](err); ok {
		resp.Code, resp.LoadedTapeMM = wire.CodeTapeMismatch, me.Loaded
		return resp
	}
	if _, ok := errors.AsType[*ptouch.ImageHeightError](err); ok {
		resp.Code = wire.CodeWrongHeight
		return resp
	}
	if _, ok := errors.AsType[*ptouch.LabelTooLongError](err); ok {
		resp.Code = wire.CodeTooLong
		return resp
	}
	switch {
	case errors.Is(err, ptouch.ErrEmptyLabel):
		resp.Code = wire.CodeEmptyLabel
	case errors.Is(err, ptouch.ErrNotConnected):
		resp.Code = wire.CodeNotConnected
	case errors.Is(err, ptouch.ErrEditorLite):
		resp.Code = wire.CodeEditorLite
	case errors.Is(err, ptouch.ErrNoReply):
		resp.Code = wire.CodeNoReply
	case errors.Is(err, ptouch.ErrBusy):
		resp.Code = wire.CodeBusy
	case errors.Is(err, ptouch.ErrUnconfirmed):
		// The label may already be on the tape, and jobs are never retried.
		resp.Code, resp.MayHavePrinted = wire.CodeUnconfirmed, true
	case errors.Is(err, context.Canceled):
		resp.Code = wire.CodeCanceled
	case errors.Is(err, ptouch.ErrPermission):
		resp.Code = wire.CodePermission
	default:
		resp.Code = wire.CodeInternal
	}
	return resp
}
