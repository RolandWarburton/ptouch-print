package ptouch

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// fakeDevice plays back status replies. Like usblp, it returns EOF once before
// each reply and forever after the last one.
type fakeDevice struct {
	replies [][]byte
	pending []byte
	eofSent bool
	written bytes.Buffer
	closed  bool
}

func (d *fakeDevice) Read(p []byte) (int, error) {
	if len(d.pending) == 0 {
		if len(d.replies) == 0 || !d.eofSent {
			d.eofSent = true
			return 0, io.EOF
		}
		d.eofSent = false
		d.pending, d.replies = d.replies[0], d.replies[1:]
	}
	n := copy(p, d.pending)
	d.pending = d.pending[n:]
	return n, nil
}

func (d *fakeDevice) Write(p []byte) (int, error) { return d.written.Write(p) }
func (d *fakeDevice) Close() error                { d.closed = true; return nil }

func reply(tapeMM int, e1, e2, statusType byte) []byte {
	b := make([]byte, statusLen)
	b[0] = 0x80
	b[8], b[9] = e1, e2
	b[10] = byte(tapeMM)
	b[11] = 0x01 // laminated
	b[18] = statusType
	return b
}

func testPrinter(dev *fakeDevice) *Printer {
	return New(func() (Device, error) { return dev, nil }, Config{
		BusyTimeout:   20 * time.Millisecond,
		StatusTimeout: 30 * time.Millisecond,
		PrintTimeout:  30 * time.Millisecond,
		PollInterval:  time.Millisecond,
		MaxLabelDots:  100,
	})
}

// blankLabel returns a white image of the given size.
func blankLabel(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	return img
}

func fixed(img image.Image) RenderFunc {
	return func(TapeSpec) (image.Image, error) { return img, nil }
}

func TestEncodeJob(t *testing.T) {
	tests := []struct {
		mm           int
		firstPinByte int
		firstPinBit  byte
		lastPinByte  int
		lastPinBit   byte
	}{
		{24, 0, 0x80, 15, 0x01}, // pins 0 and 127
		{12, 3, 0x04, 12, 0x20}, // pins 29 and 98
	}
	for _, tt := range tests {
		tape, _ := Tape(tt.mm)
		img := blankLabel(2, tape.Dots)
		img.SetGray(0, 0, color.Gray{})           // first printable row, column 0
		img.SetGray(1, tape.Dots-1, color.Gray{}) // last printable row, column 1

		job := encodeJob(img, tape, 0x01)

		header := []byte{
			0x1b, 0x69, 0x61, 0x01,
			0x1b, 0x69, 0x7a, 0x86, 0x01, byte(tt.mm), 0x00, 2, 0, 0, 0, 0x00, 0x00,
			0x1b, 0x69, 0x4d, 0x40,
			0x4d, 0x02,
		}
		if !bytes.HasPrefix(job, header) {
			t.Fatalf("%d mm: header\n got % x\nwant % x", tt.mm, job[:len(header)], header)
		}
		lines := job[len(header) : len(job)-1]
		if len(lines) != 2*(4+lineBytes) || job[len(job)-1] != 0x1a {
			t.Fatalf("%d mm: wrong job length %d or missing print command", tt.mm, len(job))
		}
		want0, want1 := make([]byte, lineBytes), make([]byte, lineBytes)
		want0[tt.firstPinByte] = tt.firstPinBit
		want1[tt.lastPinByte] = tt.lastPinBit
		for i, want := range [][]byte{want0, want1} {
			line := lines[i*(4+lineBytes):][:4+lineBytes]
			if !bytes.Equal(line[:4], []byte{'G', 17, 0, 15}) || !bytes.Equal(line[4:], want) {
				t.Errorf("%d mm line %d: got % x, want line % x", tt.mm, i, line, want)
			}
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

func TestStatus(t *testing.T) {
	dev := &fakeDevice{replies: [][]byte{reply(24, 0x00, 0x10, 0)}}
	st, err := testPrinter(dev).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.TapeMM != 24 || st.MediaType != 0x01 || !slices.Equal(st.Errors, []string{"cover open"}) {
		t.Errorf("got %+v", st)
	}
	if !bytes.Equal(dev.written.Bytes(), statusRequest()) {
		t.Errorf("wrote % x, want the status request", dev.written.Bytes())
	}
	if !dev.closed {
		t.Error("device not closed")
	}
}

func TestStatusNoReply(t *testing.T) {
	_, err := testPrinter(&fakeDevice{}).Status(context.Background())
	if !errors.Is(err, ErrNoReply) {
		t.Errorf("got %v, want ErrNoReply", err)
	}
}

func TestStatusBadReply(t *testing.T) {
	bad := make([]byte, statusLen)
	_, err := testPrinter(&fakeDevice{replies: [][]byte{bad}}).Status(context.Background())
	if err == nil {
		t.Error("expected an error for a reply without the 0x80 header")
	}
}

func TestPrint(t *testing.T) {
	dev := &fakeDevice{replies: [][]byte{reply(24, 0, 0, 0), reply(24, 0, 0, statusTypeCompleted)}}
	var rendered TapeSpec
	res, err := testPrinter(dev).Print(context.Background(), 0, func(t TapeSpec) (image.Image, error) {
		rendered = t
		return blankLabel(10, t.Dots), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if rendered.MM != 24 || res.Tape.MM != 24 || res.LengthDots != 10 {
		t.Errorf("rendered for %+v, result %+v", rendered, res)
	}
	job := dev.written.Bytes()[len(statusRequest()):]
	if !bytes.HasPrefix(job, []byte{0x1b, 0x69, 0x61, 0x01}) || job[len(job)-1] != 0x1a {
		t.Errorf("print job not sent after the status request")
	}
}

func TestPrintRefusesBeforeSending(t *testing.T) {
	tests := []struct {
		name   string
		status []byte
		wantMM int
		img    image.Image
		check  func(error) bool
	}{
		{"cover open", reply(24, 0, 0x10, 0), 0, blankLabel(10, 128), func(err error) bool {
			var pe *PrinterError
			return errors.As(err, &pe) && !pe.AfterSend && slices.Equal(pe.Errors, []string{"cover open"})
		}},
		{"unsupported tape", reply(18, 0, 0, 0), 0, blankLabel(10, 112), func(err error) bool {
			var ue *UnsupportedTapeError
			return errors.As(err, &ue) && ue.MM == 18
		}},
		{"tape mismatch", reply(24, 0, 0, 0), 12, blankLabel(10, 70), func(err error) bool {
			var me *TapeMismatchError
			return errors.As(err, &me) && me.Want == 12 && me.Loaded == 24
		}},
		{"wrong height", reply(24, 0, 0, 0), 0, blankLabel(10, 70), func(err error) bool {
			var he *ImageHeightError
			return errors.As(err, &he) && he.Got == 70 && he.Want == 128
		}},
		{"too long", reply(24, 0, 0, 0), 0, blankLabel(101, 128), func(err error) bool {
			var le *LabelTooLongError
			return errors.As(err, &le) && le.Got == 101
		}},
		{"empty", reply(24, 0, 0, 0), 0, blankLabel(0, 128), func(err error) bool {
			return errors.Is(err, ErrEmptyLabel)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev := &fakeDevice{replies: [][]byte{tt.status}}
			_, err := testPrinter(dev).Print(context.Background(), tt.wantMM, fixed(tt.img))
			if !tt.check(err) {
				t.Errorf("got error %v", err)
			}
			if !bytes.Equal(dev.written.Bytes(), statusRequest()) {
				t.Error("a print job was sent")
			}
		})
	}
}

func TestPrintMatchingTape(t *testing.T) {
	dev := &fakeDevice{replies: [][]byte{reply(12, 0, 0, 0), reply(12, 0, 0, statusTypeCompleted)}}
	if _, err := testPrinter(dev).Print(context.Background(), 12, fixed(blankLabel(5, 70))); err != nil {
		t.Fatal(err)
	}
}

func TestPrintRejectedAfterSend(t *testing.T) {
	dev := &fakeDevice{replies: [][]byte{reply(24, 0, 0, 0), reply(24, 0x02, 0, statusTypeError)}}
	_, err := testPrinter(dev).Print(context.Background(), 0, fixed(blankLabel(10, 128)))
	var pe *PrinterError
	if !errors.As(err, &pe) || !pe.AfterSend || !slices.Equal(pe.Errors, []string{"tape ran out"}) {
		t.Errorf("got %v", err)
	}
}

func TestPrintUnconfirmed(t *testing.T) {
	dev := &fakeDevice{replies: [][]byte{reply(24, 0, 0, 0)}}
	_, err := testPrinter(dev).Print(context.Background(), 0, fixed(blankLabel(10, 128)))
	if !errors.Is(err, ErrUnconfirmed) {
		t.Errorf("got %v, want ErrUnconfirmed", err)
	}
}

func TestPrintClientGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dev := &fakeDevice{replies: [][]byte{reply(24, 0, 0, 0)}}
	_, err := testPrinter(dev).Print(ctx, 0, func(t TapeSpec) (image.Image, error) {
		cancel()
		return blankLabel(10, t.Dots), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
	if !bytes.Equal(dev.written.Bytes(), statusRequest()) {
		t.Error("a print job was sent after the client went away")
	}
}

func TestBusy(t *testing.T) {
	entered, unblock := make(chan struct{}), make(chan struct{})
	p := New(func() (Device, error) {
		close(entered)
		<-unblock
		return nil, ErrNotConnected
	}, Config{BusyTimeout: 20 * time.Millisecond})

	done := make(chan error)
	go func() { _, err := p.Status(context.Background()); done <- err }()
	<-entered
	if _, err := p.Status(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("second request: got %v, want ErrBusy", err)
	}
	close(unblock)
	if err := <-done; !errors.Is(err, ErrNotConnected) {
		t.Errorf("first request: got %v", err)
	}
}

// fakeSysfs builds a sysfs tree with one USB device per id, each with a usblp
// interface when lp is true.
func fakeSysfs(t *testing.T, devs ...struct {
	id string
	lp bool
}) string {
	root := t.TempDir()
	mk := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lp := 0
	for i, d := range devs {
		name := "1-" + string(rune('1'+i))
		dev := filepath.Join(root, "devices", "usb1", name)
		mk(filepath.Join(dev, "idVendor"), d.id[:4])
		mk(filepath.Join(dev, "idProduct"), d.id[5:])
		must(t, os.MkdirAll(filepath.Join(root, "bus", "usb", "devices"), 0o755))
		must(t, os.Symlink(dev, filepath.Join(root, "bus", "usb", "devices", name)))
		if d.lp {
			iface := filepath.Join(dev, name+":1.0")
			class := filepath.Join(root, "class", "usbmisc", "lp"+string(rune('0'+lp)))
			must(t, os.MkdirAll(iface, 0o755))
			must(t, os.MkdirAll(class, 0o755))
			must(t, os.Symlink(iface, filepath.Join(class, "device")))
			lp++
		}
	}
	return root
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type usbDev = struct {
	id string
	lp bool
}

func TestFind(t *testing.T) {
	tests := []struct {
		name    string
		devs    []usbDev
		want    string
		wantErr error
	}{
		{"printer", []usbDev{{"04f9:2061", true}}, "/dev/usb/lp0", nil},
		{"after other Brother printer", []usbDev{{"04f9:2062", true}, {"04f9:2061", true}}, "/dev/usb/lp1", nil},
		{"only other Brother printer", []usbDev{{"04f9:2062", true}}, "", ErrNotConnected},
		{"editor lite", []usbDev{{"04f9:2064", false}}, "", ErrEditorLite},
		{"nothing", nil, "", ErrNotConnected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Find(fakeSysfs(t, tt.devs...), "/dev")
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Errorf("got %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
