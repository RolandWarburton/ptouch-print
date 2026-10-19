// Package wire holds what the board and the command server send each other, so
// the two binaries can't disagree about the format.
//
// The board asks for work with GET NextPath?tape_mm=N, getting a Job (200) or
// nothing (204), and reports each job it took with POST ResultPath(id). When
// the printer can't print, the poll says tape_mm=0 and adds ProblemParam with
// the reason, one of the result codes (editor_lite, printer_error, …); a poll
// without it means the printer is ready.
package wire

import (
	"fmt"
	"strconv"
)

const (
	NextPath     = "/board/next"
	ProblemParam = "problem"

	KindBarcode = "barcode"
	KindImage   = "image"

	MaxTextLen    = 80
	MaxImageBytes = 1 << 20
)

// ResultPath is where the board reports the result of job id.
func ResultPath(id int64) string { return "/board/jobs/" + strconv.FormatInt(id, 10) + "/result" }

// Job is one label for the board to print.
type Job struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"` // barcode
	// Image jobs: the tape the PNG was rendered for, and the PNG.
	TapeMM int    `json:"tape_mm,omitempty"`
	Image  []byte `json:"image,omitempty"`
}

// Result is the outcome of a job. Code is empty when the label printed;
// otherwise it is one of the error codes in the spec, and Error is for people.
type Result struct {
	Printed        bool     `json:"printed"`
	TapeMM         int      `json:"tape_mm,omitempty"`
	LengthDots     int      `json:"length_dots,omitempty"`
	Code           string   `json:"code,omitempty"`
	Error          string   `json:"error,omitempty"`
	PrinterErrors  []string `json:"printer_errors,omitempty"`
	LoadedTapeMM   int      `json:"loaded_tape_mm,omitempty"`
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
