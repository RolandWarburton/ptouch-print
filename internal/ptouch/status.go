package ptouch

import (
	"fmt"
	"strings"
)

const statusLen = 32

// Status types reported in byte 18 of a status reply.
const (
	statusTypeCompleted = 0x01
	statusTypeError     = 0x02
)

// Status is the printer's answer to a status request.
type Status struct {
	TapeMM    int      // loaded tape width; 3.5 mm tape reports as 4
	MediaType byte     // e.g. 0x01 laminated; sent back unchanged in the print job
	Errors    []string // decoded error bits, empty when the printer is ready
	Type      byte     // status type, byte 18

	e1, e2 byte
}

type errBit struct {
	bit byte
	msg string
}

// Ordered slices, not maps, so the error list comes out the same every time.
var (
	error1Bits = []errBit{{0x01, "no tape"}, {0x02, "tape ran out"}, {0x04, "cutter jammed"},
		{0x08, "battery weak"}, {0x40, "printer too hot"}, {0x80, "high voltage adapter"}}
	error2Bits = []errBit{{0x01, "wrong tape"}, {0x04, "tape cassette changed"},
		{0x10, "cover open"}, {0x20, "printer overheating"}}
)

func parseStatus(b []byte) (Status, error) {
	if len(b) < statusLen || b[0] != 0x80 {
		return Status{}, fmt.Errorf("unexpected status reply % x", b[:min(4, len(b))])
	}
	return Status{
		TapeMM:    int(b[10]),
		MediaType: b[11],
		Errors:    decodeErrors(b[8], b[9]),
		Type:      b[18],
		e1:        b[8],
		e2:        b[9],
	}, nil
}

func decodeErrors(e1, e2 byte) []string {
	errs := []string{}
	for _, e := range error1Bits {
		if e1&e.bit != 0 {
			errs = append(errs, e.msg)
		}
	}
	for _, e := range error2Bits {
		if e2&e.bit != 0 {
			errs = append(errs, e.msg)
		}
	}
	return errs
}

// PrinterError is an error the printer reported. AfterSend says whether the
// label had already been sent when it happened.
type PrinterError struct {
	Errors    []string
	AfterSend bool
	E1, E2    byte
}

func (e *PrinterError) Error() string {
	if len(e.Errors) == 0 {
		return fmt.Sprintf("printer rejected the label (error bytes %02x %02x)", e.E1, e.E2)
	}
	return "printer error: " + strings.Join(e.Errors, ", ")
}
