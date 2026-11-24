// Command pbm2png turns a PBM (P4) on stdin into a PNG on stdout, to look at a
// label's raster.
//
//	pbm2png [-crop N] [-ccw] [-scale N] < raster.pbm > raster.png
package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
)

func main() {
	crop := flag.Int("crop", 0, "keep only the first N columns")
	ccw := flag.Bool("ccw", false, "turn a quarter turn left, as a landscape label is read")
	scale := flag.Int("scale", 1, "magnify by this factor")
	flag.Parse()

	r := bufio.NewReader(os.Stdin)
	var w, h int
	if _, err := fmt.Fscanf(r, "P4\n%d %d\n", &w, &h); err != nil {
		fmt.Fprintln(os.Stderr, "not a P4 PBM:", err)
		os.Exit(1)
	}
	data, _ := io.ReadAll(r)
	stride := (w + 7) / 8
	if *crop > 0 && *crop < w {
		w = *crop
	}
	dot := func(x, y int) bool { return data[y*stride+x/8]&(0x80>>(x%8)) != 0 }

	ow, oh := w, h
	if *ccw {
		ow, oh = h, w
	}
	img := image.NewGray(image.Rect(0, 0, ow**scale+2, oh**scale+2))
	for i := range img.Pix {
		img.Pix[i] = 0xc0 // the border, so the label's edge shows
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ox, oy := x, y
			if *ccw {
				ox, oy = y, w-1-x
			}
			c := color.Gray{Y: 0xff}
			if dot(x, y) {
				c = color.Gray{}
			}
			for j := 0; j < *scale; j++ {
				for i := 0; i < *scale; i++ {
					img.SetGray(1+ox**scale+i, 1+oy**scale+j, c)
				}
			}
		}
	}
	png.Encode(os.Stdout, img)
}
