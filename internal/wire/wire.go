// Package wire holds what the boards and the command server send each other, so
// they can't disagree about the format. The LabelWriter board is firmware in
// firmware/ with its own copy of this; firmware_test.go keeps the two alike.
//
// A board asks for work with GET NextPath?media=ID, getting a Job (200) or
// nothing (204), and reports each job it took with POST ResultPath(id). When
// the printer can't print, the poll says media= (empty) and adds ProblemParam
// with the reason, one of the result codes (editor_lite, printer_error, …); a
// poll without it means the printer is ready. WiFiParam, when present, is the
// name of the Wi-Fi network the board is on. The server draws the label for the
// media the poll says, so the job is a Raster.
//
// The Raspberry Pi board this replaces sends TapeParam (tape_mm=N, or 0) in place
// of MediaParam, and gets the job as it was submitted, to draw itself.
package wire

import (
	"fmt"
	"strconv"
)

const (
	NextPath     = "/board/next"
	MediaParam   = "media"
	TapeParam    = "tape_mm"
	ProblemParam = "problem"
	WiFiParam    = "wifi"

	KindBarcode = "barcode"
	KindText    = "text"
	KindImage   = "image"

	OrientPortrait  = "portrait"  // the label's short side is the text's width
	OrientLandscape = "landscape" // the long side is

	MaxTextLen    = 80
	MaxImageBytes = 1 << 20
)

// Result codes: why a job failed, or, sent as ProblemParam, why the printer
// can't print.
const (
	CodeBadText          = "bad_text"
	CodeBadRequest       = "bad_request"
	CodeBadImage         = "bad_image"
	CodePrintFailed      = "print_failed" // the printer reported an error after the job was sent
	CodePrinterError     = "printer_error"
	CodeUnsupportedTape  = "unsupported_tape"
	CodeTapeMismatch     = "tape_mismatch"
	CodeUnsupportedMedia = "unsupported_media"
	CodeWrongMedia       = "wrong_media" // the job is for other media than is loaded
	CodeWrongHeight      = "wrong_height"
	CodeTooLong          = "too_long"
	CodeEmptyLabel       = "empty_label"
	CodeNotConnected     = "not_connected"
	CodeEditorLite       = "editor_lite"
	CodeNoReply          = "no_reply"
	CodeBusy             = "busy"
	CodeUnconfirmed      = "unconfirmed" // may have printed
	CodeCanceled         = "canceled"
	CodePermission       = "permission"
	CodeInternal         = "internal"
)

// TapeSpec is the printable area of one tape width.
type TapeSpec struct {
	MM       int
	Dots     int  // printable height in dots, across the tape
	Margin   int  // unused print head pins before the printable area
	Verified bool // geometry confirmed by printing on real tape
}

var tapes = map[int]TapeSpec{
	12: {MM: 12, Dots: 70, Margin: 29},
	24: {MM: 24, Dots: 128, Margin: 0, Verified: true},
}

// Tape returns the geometry for a supported tape width.
func Tape(mm int) (TapeSpec, bool) {
	t, ok := tapes[mm]
	return t, ok
}

// Printer families: they decide which media, label kinds and options apply.
const (
	FamilyPTouch = "ptouch"
	FamilyLW450  = "lw450"
)

// Media is one kind of label stock. Width and Length are the printable dots
// across the head and along the feed; Length is 0 for tape, which is cut to fit
// the label. Pitch is a label's length from one to the next, in dots. The
// LabelWriter's rows match media_t in firmware/components/label/label.c.
type Media struct {
	ID     string
	Name   string
	Family string
	TapeMM int // tape only
	Width  int
	Length int
	Pitch  int
}

var media = []Media{
	{ID: "tape-12", Name: "12 mm tape", Family: FamilyPTouch, TapeMM: 12, Width: tapes[12].Dots},
	{ID: "tape-24", Name: "24 mm tape", Family: FamilyPTouch, TapeMM: 24, Width: tapes[24].Dots},
	{ID: "large-address", Name: "DYMO Large Address S0722400", Family: FamilyLW450, Width: 400, Length: 950, Pitch: 1050},
}

// FindMedia looks up media that printers of family take by its id.
func FindMedia(family, id string) (Media, bool) {
	for _, m := range media {
		if m.Family == family && m.ID == id {
			return m, true
		}
	}
	return Media{}, false
}

// ImageSize is the size in dots an image for m must be. A width of 0 means any:
// an image for tape runs along it, so only its height is fixed.
func (m Media) ImageSize() (width, height int) {
	if m.Length == 0 {
		return 0, m.Width
	}
	return m.Width, m.Length
}

// ResultPath is where the board reports the result of job id.
func ResultPath(id int64) string { return "/board/jobs/" + strconv.FormatInt(id, 10) + "/result" }

// Job is one label for a board to print.
type Job struct {
	ID      int64  `json:"id"`
	Printer string `json:"printer,omitempty"` // which printer; only on submit, as a board only gets its own jobs
	Kind    string `json:"kind"`
	// The media the job is for. Always set for a LabelWriter; for a P-touch, only
	// when whoever submitted it said so.
	Media       string `json:"media,omitempty"`
	Orientation string `json:"orientation,omitempty"` // LabelWriter text and barcodes: portrait or landscape
	Text        string `json:"text,omitempty"`        // barcode
	// Text jobs: one or two lines on tape, up to eight on a LabelWriter, drawn for
	// the loaded media.
	Lines []string `json:"lines,omitempty"`
	Size  string   `json:"size,omitempty"`  // large, medium or small
	Align string   `json:"align,omitempty"` // left, center or right
	// Image jobs: the PNG, and for a P-touch the tape it was rendered for.
	TapeMM int    `json:"tape_mm,omitempty"`
	Image  []byte `json:"image,omitempty"`
	// What a board that polls with MediaParam gets in place of the fields above.
	Raster *Raster `json:"raster,omitempty"`
}

// Raster is a drawn label: Rows rows of Dots dots each across the print head, in
// feed order. A row is (Dots+7)/8 bytes; bit 7 of the first is the first dot, and
// 1 is black. The board must not print it on any media but Media.
type Raster struct {
	Media string `json:"media"`
	Dots  int    `json:"dots"`
	Rows  int    `json:"rows"`
	Data  []byte `json:"data"`
}

// Result is the outcome of a job. Code is empty when the label printed;
// otherwise it is one of the Code constants, and Error is for people.
type Result struct {
	Printed        bool     `json:"printed"`
	TapeMM         int      `json:"tape_mm,omitempty"`
	LengthDots     int      `json:"length_dots,omitempty"`
	Code           string   `json:"code,omitempty"`
	Error          string   `json:"error,omitempty"`
	PrinterErrors  []string `json:"printer_errors,omitempty"`
	LoadedTapeMM   int      `json:"loaded_tape_mm,omitempty"`
	LoadedMedia    string   `json:"loaded_media,omitempty"`
	MayHavePrinted bool     `json:"may_have_printed,omitempty"`
}

// Failed is a Result for a job that didn't print.
func Failed(code, msg string) Result { return Result{Code: code, Error: msg} }

// ValidateText checks barcode text, returning a reason when it's unusable.
func ValidateText(text string) string {
	if text == "" || len(text) > MaxTextLen {
		return fmt.Sprintf("text must be 1 to %d characters", MaxTextLen)
	}
	for _, c := range []byte(text) {
		if c < 0x20 || c > 0x7e {
			return "text must be printable ASCII"
		}
	}
	return ""
}
