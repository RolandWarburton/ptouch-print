package label

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

// The labels in testdata/diecut.txt, with the SHA-256 of their rows, are as the
// LabelWriter firmware drew them itself before the server did. "error N" is one
// it refused: 1 bad text, 2 too long.
func TestDieCutMatchesFirmware(t *testing.T) {
	b, err := os.ReadFile("testdata/diecut.txt")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := wire.FindMedia(wire.FamilyLW450, "large-address")
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(line, "\t")
		want, args := f[0], f[1:]
		job := wire.Job{Kind: args[0], Orientation: args[1]}
		if job.Kind == wire.KindText {
			job.Size, job.Align, job.Lines = args[2], args[3], args[4:]
		} else {
			job.Text = args[2]
		}
		r, err := Draw(job, m)
		if strings.HasPrefix(want, "error") {
			var te *TextError
			if !errors.As(err, &te) || te.Code != map[string]string{"error 1": wire.CodeBadText, "error 2": wire.CodeTooLong}[want] {
				t.Errorf("%q: %v, want %s", args, err, want)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", args, err)
			continue
		}
		sum := sha256.Sum256(r.Data)
		if r.Dots != 400 || r.Rows != 950 || r.Media != m.ID || hex.EncodeToString(sum[:]) != want {
			t.Errorf("%q: %dx%d differs from the firmware's", args, r.Dots, r.Rows)
		}
	}
}

func TestDrawTape(t *testing.T) {
	m, _ := wire.FindMedia(wire.FamilyPTP700, "tape-12")
	img := image.NewNRGBA(image.Rect(0, 0, 3, 70)) // transparent
	img.Set(0, 0, color.Black)
	img.Set(1, 9, color.Gray{0x40})
	img.Set(2, 69, color.Black)
	img.Set(2, 1, color.Gray{0xc0})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	r, err := Draw(wire.Job{Kind: wire.KindImage, Image: b.Bytes()}, m)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 27)
	want[0], want[9+1], want[18+8] = 0x80, 0x40, 0x04
	if r.Media != "tape-12" || r.Dots != 70 || r.Rows != 3 || !bytes.Equal(r.Data, want) {
		t.Errorf("got %+v", r)
	}

	// Text and barcodes are the pictures Text and Barcode draw.
	r, err = Draw(wire.Job{Kind: wire.KindBarcode, Text: "TG17573"}, m)
	if err != nil {
		t.Fatal(err)
	}
	bc, _ := Barcode("TG17573", 70)
	if r.Rows != bc.Bounds().Dx() || r.Dots != 70 {
		t.Errorf("barcode is %dx%d", r.Dots, r.Rows)
	}
	for y := range r.Rows {
		for x := range r.Dots {
			if got := r.Data[y*9+x/8]&(0x80>>(x%8)) != 0; got != dark(bc.At(y, x)) {
				t.Fatalf("row %d dot %d is %v", y, x, got)
			}
		}
	}
}

func TestDrawRefuses(t *testing.T) {
	tape, _ := wire.FindMedia(wire.FamilyPTP700, "tape-24")
	labels, _ := wire.FindMedia(wire.FamilyLW450, "large-address")
	pngOf := func(w, h int) []byte {
		var b bytes.Buffer
		png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
		return b.Bytes()
	}
	for _, tt := range []struct {
		name string
		job  wire.Job
		m    wire.Media
		code string
	}{
		{"not a PNG", wire.Job{Kind: wire.KindImage, Image: []byte("x")}, tape, wire.CodeBadImage},
		{"cut short", wire.Job{Kind: wire.KindImage, Image: pngOf(50, 128)[:60]}, tape, wire.CodeBadImage},
		{"height", wire.Job{Kind: wire.KindImage, Image: pngOf(50, 70)}, tape, wire.CodeWrongHeight},
		{"length", wire.Job{Kind: wire.KindImage, Image: pngOf(3601, 128)}, tape, wire.CodeTooLong},
		{"label size", wire.Job{Kind: wire.KindImage, Image: pngOf(401, 950)}, labels, wire.CodeWrongHeight},
		{"long text", wire.Job{Kind: wire.KindText, Lines: []string{strings.Repeat("W", 60)}}, tape, wire.CodeTooLong},
		{"glyph", wire.Job{Kind: wire.KindText, Lines: []string{"日"}}, tape, wire.CodeBadText},
		{"kind", wire.Job{Kind: "poem"}, tape, wire.CodeBadRequest},
	} {
		_, err := Draw(tt.job, tt.m)
		var te *TextError
		if !errors.As(err, &te) || te.Code != tt.code {
			t.Errorf("%s: %v, want %s", tt.name, err, tt.code)
		}
	}
}

func TestIsBlack(t *testing.T) {
	for _, tt := range []struct {
		c    color.Color
		want bool
	}{
		{color.Black, true},
		{color.White, false},
		{color.Transparent, false}, // blank tape, not black
		{color.RGBA{0x40, 0x40, 0x40, 0xff}, true},
		{color.RGBA{0xc0, 0xc0, 0xc0, 0xff}, false},
	} {
		if got := isBlack(tt.c); got != tt.want {
			t.Errorf("isBlack(%v) = %v, want %v", tt.c, got, tt.want)
		}
	}
}
