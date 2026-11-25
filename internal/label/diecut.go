package label

import (
	"image"
	"image/color"
	"image/draw"

	"github.com/boombuler/barcode/code128"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

// Labels on a LabelWriter's die-cut stock, at 300 dpi.
const (
	dieCutPad     = 10  // blank dots kept clear at each edge
	dieCutQuiet   = 20  // blank dots either side of a barcode, for scanners
	dieCutBars    = 200 // a barcode's height
	dieCutCaption = 36  // the size of the text under it
	dieCutGap     = 12  // between the two
)

var dieCutSizes = map[string]float64{"large": 80, "medium": 54, "small": 36}

// dieCutCanvas is a blank label as it is read, and dieCutImage turns it into what
// the print head gets.
func dieCutCanvas(m wire.Media, orientation string) *image.Gray {
	w, h := m.Width, m.Length
	if orientation == wire.OrientLandscape {
		w, h = h, w
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	return img
}

func dieCutImage(canvas *image.Gray, orientation string) image.Image {
	if orientation == wire.OrientLandscape {
		return turned{canvas}
	}
	return canvas
}

// turned is an image turned a quarter turn right: turn the label a quarter turn
// left to read it.
type turned struct{ *image.Gray }

func (t turned) Bounds() image.Rectangle {
	b := t.Gray.Bounds()
	return image.Rect(0, 0, b.Dy(), b.Dx())
}

func (t turned) At(x, y int) color.Color { return t.Gray.At(y, t.Gray.Bounds().Dy()-1-x) }

// dotFont draws a face on whole dots, with no grey.
type dotFont struct {
	face            font.Face
	ascent, descent int
}

func newDotFont(parse func() (*opentype.Font, error), size float64) (*dotFont, error) {
	f, err := parse()
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	m := face.Metrics()
	return &dotFont{face, m.Ascent.Ceil(), m.Descent.Ceil()}, nil
}

func (f *dotFont) width(s string) int {
	w := 0
	for _, r := range s {
		adv, _ := f.face.GlyphAdvance(r)
		w += adv.Round()
	}
	return w
}

func (f *dotFont) draw(img *image.Gray, s string, x, baseline int) {
	for _, r := range s {
		dr, mask, maskp, adv, ok := f.face.Glyph(fixed.P(x, baseline), r)
		if !ok {
			continue
		}
		for y := dr.Min.Y; y < dr.Max.Y; y++ {
			for i := dr.Min.X; i < dr.Max.X; i++ {
				if _, _, _, a := mask.At(maskp.X+i-dr.Min.X, maskp.Y+y-dr.Min.Y).RGBA(); a >= 0x8000 {
					img.SetGray(i, y, color.Gray{})
				}
			}
		}
		x += adv.Round()
	}
}

// dieCutText draws lines of text, centred vertically.
func dieCutText(t TextLabel, m wire.Media, orientation string) (image.Image, error) {
	t, err := CheckDieCutText(t)
	if err != nil {
		return nil, err
	}
	f, err := newDotFont(parseTextFont, dieCutSizes[t.Size])
	if err != nil {
		return nil, err
	}
	defer f.face.Close()
	img := dieCutCanvas(m, orientation)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()

	for i, l := range t.Lines {
		if lw := f.width(l); lw > w-2*dieCutPad {
			return nil, textErr(wire.CodeTooLong, "line %d is %d dots wide; the label fits %d", i+1, lw, w-2*dieCutPad)
		}
	}
	lineHeight := f.ascent + f.descent
	gap := lineHeight / 10
	block := len(t.Lines)*(lineHeight+gap) - gap
	if block > h-2*dieCutPad {
		return nil, textErr(wire.CodeTooLong, "%d lines are %d dots tall; the label fits %d", len(t.Lines), block, h-2*dieCutPad)
	}
	for i, l := range t.Lines {
		x := dieCutPad
		switch lw := f.width(l); t.Align {
		case "center":
			x = (w - lw) / 2
		case "right":
			x = w - dieCutPad - lw
		}
		f.draw(img, l, x, (h-block)/2+i*(lineHeight+gap)+f.ascent)
	}
	return dieCutImage(img, orientation), nil
}

// dieCutBarcode draws a Code 128 barcode with its text beneath it, centred.
func dieCutBarcode(text string, m wire.Media, orientation string) (image.Image, error) {
	if msg := wire.ValidateText(text); msg != "" {
		return nil, textErr(wire.CodeBadText, "%s", msg)
	}
	bc, err := code128.Encode(text)
	if err != nil {
		return nil, textErr(wire.CodeBadText, "can't make a Code 128 barcode of that: %v", err)
	}
	f, err := newDotFont(parseFont, dieCutCaption)
	if err != nil {
		return nil, err
	}
	defer f.face.Close()
	img := dieCutCanvas(m, orientation)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()

	modules, fits := bc.Bounds().Dx(), w-2*dieCutQuiet
	dots := 2
	if modules*3 <= fits { // the widest bars that fit scan best
		dots = 3
	}
	if modules*dots > fits {
		return nil, textErr(wire.CodeTooLong, "the barcode is %d dots wide; the label fits %d", modules*dots, fits)
	}
	tw := f.width(text)
	if tw > w-2*dieCutPad {
		return nil, textErr(wire.CodeTooLong, "the text is %d dots wide; the label fits %d", tw, w-2*dieCutPad)
	}
	block := dieCutBars + dieCutGap + f.ascent + f.descent
	if block > h-2*dieCutPad {
		return nil, textErr(wire.CodeTooLong, "the barcode is %d dots tall; the label fits %d", block, h-2*dieCutPad)
	}
	top, left := (h-block)/2, (w-modules*dots)/2
	for x := range modules * dots {
		if r, _, _, _ := bc.At(x/dots, 0).RGBA(); r < 0x8000 {
			for y := range dieCutBars {
				img.SetGray(left+x, top+y, color.Gray{})
			}
		}
	}
	f.draw(img, text, (w-tw)/2, top+dieCutBars+dieCutGap+f.ascent)
	return dieCutImage(img, orientation), nil
}
