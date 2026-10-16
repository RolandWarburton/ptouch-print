// Package label draws labels for the P-touch.
package label

import (
	"image"
	"image/color"
	"image/draw"
	"sync"

	"github.com/boombuler/barcode/code128"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	moduleDots = 2  // width of the thinnest bar, in dots (~0.28 mm)
	quietZone  = 20 // blank dots either side of the barcode, for scanners
)

var parseFont = sync.OnceValues(func() (*opentype.Font, error) {
	return opentype.Parse(gomonobold.TTF)
})

// Barcode draws a Code 128 barcode of text with text printed beneath it, as a
// black-on-white image exactly height dots tall.
func Barcode(text string, height int) (image.Image, error) {
	bc, err := code128.Encode(text)
	if err != nil {
		return nil, err
	}
	f, err := parseFont()
	if err != nil {
		return nil, err
	}

	textHeight := height * 3 / 10
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size: float64(textHeight), DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, err
	}
	defer face.Close()
	textWidth := font.MeasureString(face, text).Ceil()

	barWidth := bc.Bounds().Dx() * moduleDots
	width := max(barWidth, textWidth) + 2*quietZone
	img := image.NewGray(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)

	barHeight := height - textHeight - height/20
	barLeft := (width - barWidth) / 2
	for x := 0; x < barWidth; x++ {
		if r, _, _, _ := bc.At(x/moduleDots, 0).RGBA(); r < 0x8000 {
			for y := 0; y < barHeight; y++ {
				img.SetGray(barLeft+x, y, color.Gray{})
			}
		}
	}

	d := font.Drawer{
		Dst:  img,
		Src:  image.Black,
		Face: face,
		Dot:  fixed.P((width-textWidth)/2, height-face.Metrics().Descent.Ceil()),
	}
	d.DrawString(text)
	return img, nil
}
