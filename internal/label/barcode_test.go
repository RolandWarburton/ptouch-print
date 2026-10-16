package label

import (
	"image"
	"image/color"
	"testing"
)

func TestBarcode(t *testing.T) {
	for _, height := range []int{70, 128} {
		img, err := Barcode("F2LXK0ABHG7F", height)
		if err != nil {
			t.Fatal(err)
		}
		b := img.Bounds()
		if b.Dy() != height {
			t.Errorf("height %d: image is %d dots high", height, b.Dy())
		}
		for y := 0; y < height; y++ {
			for _, x := range []int{0, quietZone - 1, b.Dx() - quietZone, b.Dx() - 1} {
				if dark(img.At(x, y)) {
					t.Fatalf("height %d: quiet zone not blank at %d,%d", height, x, y)
				}
			}
		}
		if !hasBar(img) {
			t.Errorf("height %d: no bars drawn", height)
		}
	}
}

func hasBar(img image.Image) bool {
	for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
		if dark(img.At(x, 0)) {
			return true
		}
	}
	return false
}

func dark(c color.Color) bool {
	return color.GrayModel.Convert(c).(color.Gray).Y < 0x80
}
