package label

import (
	"fmt"
	"image"
	"image/draw"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

const (
	textPad     = 14   // blank dots at each end
	maxTextDots = 1063 // 15 cm at 180 dpi, padding included
	lineGap     = 0.1  // between two lines, as a fraction of a line's height
	twoLineMM   = 24   // the narrowest tape that takes a second line
	metricsSize = 100  // the size the font's proportions are measured at
)

var sizes = map[string]float64{"large": 1, "medium": 0.75, "small": 0.5}

var aligns = []string{"left", "center", "right"}

var parseTextFont = sync.OnceValues(func() (*opentype.Font, error) {
	return opentype.Parse(gobold.TTF)
})

// TextLabel is what a text label says and how it's laid out.
type TextLabel struct {
	Lines []string
	Size  string // large, medium or small; "" is large
	Align string // left, center or right; "" is left
}

// TextError is a text label that can't be printed. Code is a wire result code.
type TextError struct{ Code, Msg string }

func (e *TextError) Error() string { return e.Msg }

func textErr(code, format string, args ...any) *TextError {
	return &TextError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// CheckText cleans a text label and checks what can be checked without
// drawing it, for tapeMM tape. Its errors are *TextError. Control characters
// are removed and each line trimmed; the size and alignment get their defaults.
func CheckText(t TextLabel, tapeMM int) (TextLabel, error) {
	if len(t.Lines) == 0 || len(t.Lines) > 2 {
		return TextLabel{}, textErr(wire.CodeBadText, "a text label has one or two lines")
	}
	if len(t.Lines) == 2 && tapeMM < twoLineMM {
		return TextLabel{}, textErr(wire.CodeBadText, "two lines need %d mm tape; %d mm is loaded", twoLineMM, tapeMM)
	}
	clean := TextLabel{Size: t.Size, Align: t.Align}
	for i, l := range t.Lines {
		l = strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, l))
		if l == "" {
			return TextLabel{}, textErr(wire.CodeBadText, "line %d is empty", i+1)
		}
		clean.Lines = append(clean.Lines, l)
	}
	if clean.Size == "" {
		clean.Size = "large"
	}
	if _, ok := sizes[clean.Size]; !ok {
		return TextLabel{}, textErr(wire.CodeBadRequest, `size must be "large", "medium" or "small"`)
	}
	if clean.Align == "" {
		clean.Align = "left"
	}
	if !slices.Contains(aligns, clean.Align) {
		return TextLabel{}, textErr(wire.CodeBadRequest, `align must be "left", "center" or "right"`)
	}
	return clean, nil
}

// Text draws a text label in Go Bold for tape, as a black-on-white image
// exactly tape.Dots tall. Its errors are *TextError, apart from font failures.
func Text(t TextLabel, tape wire.TapeSpec) (image.Image, error) {
	t, err := CheckText(t, tape.MM)
	if err != nil {
		return nil, err
	}
	f, err := parseTextFont()
	if err != nil {
		return nil, err
	}
	if missing, err := missingGlyphs(f, t.Lines); err != nil {
		return nil, err
	} else if len(missing) > 0 {
		return nil, textErr(wire.CodeBadText, "Go Bold can't draw: %s", strings.Join(missing, " "))
	}

	// The font's ascent and descent, as fractions of its size.
	m, err := f.Metrics(nil, fixed.I(metricsSize), font.HintingNone)
	if err != nil {
		return nil, err
	}
	ascent := float64(m.Ascent) / 64 / metricsSize
	descent := float64(m.Descent) / 64 / metricsSize

	n := float64(len(t.Lines))
	block := n + (n-1)*lineGap // the lines and the gap between them, in line heights
	lineHeight := float64(tape.Dots) / block * sizes[t.Size]
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size: lineHeight / (ascent + descent), DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, err
	}
	defer face.Close()

	widths := make([]int, len(t.Lines))
	for i, l := range t.Lines {
		widths[i] = font.MeasureString(face, l).Ceil()
	}
	width := slices.Max(widths)
	if total := width + 2*textPad; total > maxTextDots {
		return nil, textErr(wire.CodeTooLong, "the label is %.1f cm long; the limit is 15 cm", float64(total)*2.54/180)
	}

	img := image.NewGray(image.Rect(0, 0, width+2*textPad, tape.Dots))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	top := (float64(tape.Dots) - lineHeight*block) / 2
	for i, l := range t.Lines {
		spare := width - widths[i]
		x := textPad + map[string]int{"left": 0, "center": spare / 2, "right": spare}[t.Align]
		y := top + float64(i)*lineHeight*(1+lineGap) + lineHeight*ascent/(ascent+descent)
		d := font.Drawer{Dst: img, Src: image.Black, Face: face, Dot: fixed.P(x, int(math.Round(y)))}
		d.DrawString(l)
	}
	return img, nil
}

// missingGlyphs returns the characters in lines that f has no glyph for, each
// once, in the order they first appear.
func missingGlyphs(f *opentype.Font, lines []string) ([]string, error) {
	var (
		buf     sfnt.Buffer
		missing []string
	)
	for _, r := range strings.Join(lines, "") {
		g, err := f.GlyphIndex(&buf, r)
		if err != nil {
			return nil, err
		}
		if g == 0 && !slices.Contains(missing, string(r)) {
			missing = append(missing, string(r))
		}
	}
	return missing, nil
}
