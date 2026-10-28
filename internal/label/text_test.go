package label

import (
	"errors"
	"image"
	"strings"
	"testing"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

var (
	tape12, _ = wire.Tape(12)
	tape24, _ = wire.Tape(24)
)

func TestText(t *testing.T) {
	for _, tt := range []struct {
		tape  wire.TapeSpec
		label TextLabel
	}{
		{tape12, TextLabel{Lines: []string{"Shelf A"}}},
		{tape24, TextLabel{Lines: []string{"Shelf A"}, Size: "small"}},
		{tape24, TextLabel{Lines: []string{"Shelf A", "Bin 3"}, Align: "right"}},
	} {
		img, err := Text(tt.label, tt.tape)
		if err != nil {
			t.Fatalf("%v: %v", tt.label, err)
		}
		b := img.Bounds()
		if b.Dy() != tt.tape.Dots {
			t.Errorf("%v: image is %d dots high, want %d", tt.label, b.Dy(), tt.tape.Dots)
		}
		left, right, _, bottom := inked(img)
		if bottom < 0 {
			t.Fatalf("%v: no ink drawn", tt.label)
		}
		if left < textPad || right >= b.Dx()-textPad {
			t.Errorf("%v: ink from x=%d to %d, inside the %d-dot padding of a %d-dot label", tt.label, left, right, textPad, b.Dx())
		}
	}
}

func TestTextSizeAndAlign(t *testing.T) {
	height := func(size string) int {
		img, err := Text(TextLabel{Lines: []string{"Hg"}, Size: size}, tape24)
		if err != nil {
			t.Fatal(err)
		}
		_, _, top, bottom := inked(img)
		return bottom - top
	}
	if l, m, s := height("large"), height("medium"), height("small"); !(l > m && m > s) {
		t.Errorf("ink heights large %d, medium %d, small %d; want decreasing", l, m, s)
	}

	// The short line of two sits at the left, middle or right of the long one.
	leftOf := func(align string) int {
		img, err := Text(TextLabel{Lines: []string{"WWWWWW", "i"}, Align: align}, tape24)
		if err != nil {
			t.Fatal(err)
		}
		left, _, _, _ := inked(img.(*image.Gray).SubImage(image.Rect(0, img.Bounds().Dy()/2, img.Bounds().Dx(), img.Bounds().Dy())))
		return left
	}
	if l, c, r := leftOf("left"), leftOf("center"), leftOf("right"); !(l < c && c < r) {
		t.Errorf("second line starts at %d (left), %d (center), %d (right); want increasing", l, c, r)
	}
}

func TestCheckText(t *testing.T) {
	got, err := CheckText(TextLabel{Lines: []string{"  Shelf\tA\n ", "Bin 3"}}, 24)
	if err != nil {
		t.Fatal(err)
	}
	if want := (TextLabel{Lines: []string{"ShelfA", "Bin 3"}, Size: "large", Align: "left"}); !equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestTextRefused(t *testing.T) {
	for _, tt := range []struct {
		name  string
		label TextLabel
		tape  wire.TapeSpec
		code  string
	}{
		{"no lines", TextLabel{}, tape24, wire.CodeBadText},
		{"three lines", TextLabel{Lines: []string{"a", "b", "c"}}, tape24, wire.CodeBadText},
		{"empty after trimming", TextLabel{Lines: []string{"A", " \n "}}, tape24, wire.CodeBadText},
		{"two lines on 12 mm", TextLabel{Lines: []string{"A", "B"}}, tape12, wire.CodeBadText},
		{"no glyph", TextLabel{Lines: []string{"tick ✓"}}, tape24, wire.CodeBadText},
		{"bad size", TextLabel{Lines: []string{"A"}, Size: "huge"}, tape24, wire.CodeBadRequest},
		{"bad align", TextLabel{Lines: []string{"A"}, Align: "middle"}, tape24, wire.CodeBadRequest},
		{"over 15 cm", TextLabel{Lines: []string{strings.Repeat("W", 40)}}, tape24, wire.CodeTooLong},
	} {
		_, err := Text(tt.label, tt.tape)
		te, ok := errors.AsType[*TextError](err)
		if !ok || te.Code != tt.code {
			t.Errorf("%s: got %v, want code %s", tt.name, err, tt.code)
		}
	}
}

func TestTextMissingGlyphsListed(t *testing.T) {
	_, err := Text(TextLabel{Lines: []string{"✓ ok ✓ ★"}}, tape24)
	if err == nil || !strings.HasSuffix(err.Error(), "can't draw: ✓ ★") {
		t.Errorf("got %v", err)
	}
}

// inked returns the bounds of the dark pixels in img.
func inked(img image.Image) (left, right, top, bottom int) {
	b := img.Bounds()
	left, top, right, bottom = b.Max.X, b.Max.Y, -1, -1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if dark(img.At(x, y)) {
				left, right = min(left, x), max(right, x)
				top, bottom = min(top, y), max(bottom, y)
			}
		}
	}
	return left, right, top, bottom
}

func equal(a, b TextLabel) bool {
	return strings.Join(a.Lines, "\x00") == strings.Join(b.Lines, "\x00") && a.Size == b.Size && a.Align == b.Align
}
