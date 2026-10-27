package ptouch

// Raster encoding for the Brother PT-P700, following Brother's P-touch raster
// command reference.

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

const (
	DPI       = 180
	HeadPins  = 128 // print head height in dots
	lineBytes = HeadPins / 8
)

// statusRequest invalidates any half-received job, initialises the printer
// and asks for its status.
func statusRequest() []byte {
	return append(make([]byte, 100), 0x1b, 0x40, 0x1b, 0x69, 0x53)
}

// encodeJob builds a print job for img, whose height must be t.Dots and whose
// width is the label length. Each image column is one raster line. It sends
// only the setup commands the PT-P700 needs; the extra ones from Brother's
// newer-model reference make it reject the job.
func encodeJob(img image.Image, t wire.TapeSpec, mediaType byte) []byte {
	b := img.Bounds()
	lines := b.Dx()

	var buf bytes.Buffer
	buf.Grow(24 + lines*(4+lineBytes))
	buf.Write([]byte{0x1b, 0x69, 0x61, 0x01}) // raster mode
	// Print information: without it the printer assumes a default tape and
	// fails with "replace media". Flags: recover | width valid | media valid.
	buf.Write([]byte{0x1b, 0x69, 0x7a, 0x86, mediaType, byte(t.MM), 0x00})
	buf.Write(binary.LittleEndian.AppendUint32(nil, uint32(lines)))
	buf.Write([]byte{0x00, 0x00})
	// Auto cut: feed and cut the leading tape before printing, then cut after.
	buf.Write([]byte{0x1b, 0x69, 0x4d, 0x40})
	buf.Write([]byte{0x4d, 0x02}) // TIFF (packbits) compression

	for x := b.Min.X; x < b.Max.X; x++ {
		var line [lineBytes]byte
		for y := 0; y < t.Dots; y++ {
			if isBlack(img.At(x, b.Min.Y+y)) {
				pin := t.Margin + y
				line[pin/8] |= 0x80 >> (pin % 8)
			}
		}
		// One packbits literal run: header 15 means "the next 16 bytes as-is".
		buf.Write([]byte{'G', lineBytes + 1, 0x00, lineBytes - 1})
		buf.Write(line[:])
	}
	buf.WriteByte(0x1a) // print with feed
	return buf.Bytes()
}

// isBlack reports whether a pixel is darker than 50% grey once composited
// onto white tape, so transparent pixels print as blank.
func isBlack(c color.Color) bool {
	r, g, b, a := c.RGBA()
	y := (19595*r + 38470*g + 7471*b + 1<<15) >> 16
	return y+(0xffff-a) < 0x8000
}
