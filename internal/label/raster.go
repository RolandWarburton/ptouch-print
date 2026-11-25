package label

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

// maxTapeDots is the longest label on tape, about 50 cm at 180 dpi.
const maxTapeDots = 3600

// Draw draws a job for the loaded media, as the rows its board prints. A job that
// can't be drawn is a *TextError.
func Draw(job wire.Job, m wire.Media) (*wire.Raster, error) {
	var (
		img image.Image
		err error
	)
	text := TextLabel{Lines: job.Lines, Size: job.Size, Align: job.Align}
	tape := m.Length == 0
	switch {
	case job.Kind == wire.KindImage:
		img, err = decodePNG(job.Image, m)
	case job.Kind == wire.KindBarcode && tape:
		img, err = Barcode(job.Text, m.Width)
	case job.Kind == wire.KindText && tape:
		spec, ok := wire.Tape(m.TapeMM)
		if !ok {
			return nil, textErr(wire.CodeUnsupportedTape, "unsupported tape width %d mm", m.TapeMM)
		}
		img, err = Text(text, spec)
	case job.Kind == wire.KindBarcode:
		img, err = dieCutBarcode(job.Text, m, job.Orientation)
	case job.Kind == wire.KindText:
		img, err = dieCutText(text, m, job.Orientation)
	default:
		return nil, textErr(wire.CodeBadRequest, "unknown job kind %q", job.Kind)
	}
	if err != nil {
		return nil, err
	}
	if !tape {
		return rows(img, m), nil
	}
	switch n := img.Bounds().Dx(); {
	case n == 0:
		return nil, textErr(wire.CodeEmptyLabel, "label is empty")
	case n > maxTapeDots:
		return nil, textErr(wire.CodeTooLong, "label is %d dots long, the maximum is %d", n, maxTapeDots)
	}
	return rows(sideways{img}, m), nil
}

// decodePNG checks the dimensions before decoding, so a small file can't expand
// into a huge image.
func decodePNG(b []byte, m wire.Media) (image.Image, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, textErr(wire.CodeBadImage, "image must be a PNG")
	}
	width, height := m.ImageSize()
	if cfg.Height != height || width != 0 && cfg.Width != width {
		return nil, textErr(wire.CodeWrongHeight, "image is %dx%d dots, which isn't the size of %s", cfg.Width, cfg.Height, m.Name)
	}
	if cfg.Width > maxTapeDots {
		return nil, textErr(wire.CodeTooLong, "image is %d dots long, the maximum is %d", cfg.Width, maxTapeDots)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, textErr(wire.CodeBadImage, "image must be a PNG")
	}
	return img, nil
}

// sideways swaps an image's axes: a label on tape is drawn along the tape, and
// each of its columns is a row for the print head.
type sideways struct{ image.Image }

func (s sideways) Bounds() image.Rectangle {
	b := s.Image.Bounds()
	return image.Rect(b.Min.Y, b.Min.X, b.Max.Y, b.Max.X)
}

func (s sideways) At(x, y int) color.Color { return s.Image.At(y, x) }

// rows packs an image whose rows are the print head's.
func rows(img image.Image, m wire.Media) *wire.Raster {
	b := img.Bounds()
	rowBytes := (b.Dx() + 7) / 8
	r := &wire.Raster{Media: m.ID, Dots: b.Dx(), Rows: b.Dy(), Data: make([]byte, rowBytes*b.Dy())}
	for y := range b.Dy() {
		for x := range b.Dx() {
			if isBlack(img.At(b.Min.X+x, b.Min.Y+y)) {
				r.Data[y*rowBytes+x/8] |= 0x80 >> (x % 8)
			}
		}
	}
	return r
}

// isBlack reports whether a pixel is darker than 50% grey once composited onto
// white, so transparent pixels print as blank.
func isBlack(c color.Color) bool {
	r, g, b, a := c.RGBA()
	y := (19595*r + 38470*g + 7471*b + 1<<15) >> 16
	return y+(0xffff-a) < 0x8000
}
