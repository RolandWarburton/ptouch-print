package server

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/RolandWarburton/ptouch-print/internal/store"
	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

// Printer is a target jobs can name. Its board's token says which printer a
// poll or result is for, so a board only ever gets its own jobs.
type Printer struct {
	Name      string
	Family    string // wire.Family*
	TokenHash string // SHA-256 of its board's token
}

var printerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// NewPrinter checks a printer's settings.
func NewPrinter(name, family, tokenHash string) (Printer, error) {
	switch {
	case !printerName.MatchString(name):
		return Printer{}, fmt.Errorf("printer name %q must be lower-case letters, digits and dashes, at most 32", name)
	case family != wire.FamilyPTouch && family != wire.FamilyLW450:
		return Printer{}, fmt.Errorf("printer %s: family %q must be %s or %s", name, family, wire.FamilyPTouch, wire.FamilyLW450)
	case !validHash(tokenHash):
		return Printer{}, fmt.Errorf("printer %s: the token hash must be 64 hex characters (see ptouch-command-server token)", name)
	}
	return Printer{name, family, strings.ToLower(tokenHash)}, nil
}

// ParsePrinters reads "name:family:sha256,name:family:sha256", in the order
// they're shown. boardHash, if set, is the token of a P-touch called "ptouch",
// which comes first: the setting from before there could be several printers.
func ParsePrinters(list, boardHash string) ([]Printer, error) {
	if boardHash != "" {
		list = store.FirstPrinter + ":" + wire.FamilyPTouch + ":" + boardHash + "," + list
	}
	var printers []Printer
	names, hashes := map[string]bool{}, map[string]bool{}
	for entry := range strings.SplitSeq(list, ",") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("%q isn't name:family:sha256-hex", entry)
		}
		p, err := NewPrinter(parts[0], parts[1], parts[2])
		if err != nil {
			return nil, err
		}
		if names[p.Name] {
			return nil, fmt.Errorf("printer name %q is used twice", p.Name)
		}
		if hashes[p.TokenHash] {
			return nil, fmt.Errorf("printer %s: another printer has the same token; each needs its own", p.Name)
		}
		names[p.Name], hashes[p.TokenHash] = true, true
		printers = append(printers, p)
	}
	return printers, nil
}

// Poll is what a board says when it asks for a job: the loaded media's id, or
// "" and why the printer can't print, and the Wi-Fi network it's on.
type Poll struct {
	Media   string
	Problem string
	WiFi    string
}

// printerState is a printer and its board's last poll. The Service's mutex
// guards what changes.
type printerState struct {
	Printer
	lastPoll time.Time
	media    wire.Media // set only when there's no problem
	problem  string     // "" when it can print
	wifi     string
}

// problems says how to fix each reason a board gives for not printing.
var problems = map[string]string{
	wire.CodeEditorLite:       "The printer is in Editor Lite mode. Hold its Editor Lite button until the light goes off.",
	wire.CodeNotConnected:     "The board can't find the printer. Check it's switched on and plugged into the board's data USB port.",
	wire.CodePrinterError:     "The printer reports an error, such as its cover open or no tape. Check the printer.",
	wire.CodeUnsupportedTape:  "The loaded tape isn't supported. Load 12 or 24 mm tape.",
	wire.CodeUnsupportedMedia: "The board says it has media this server doesn't know for the printer. Check the board's media setting.",
	wire.CodePermission:       "The board isn't allowed to open the printer. That's a setup problem on the board (its udev rule or lp group).",
	wire.CodeNoReply:          "The printer didn't answer the board. Try switching it off and on.",
	wire.CodeBusy:             "The printer is busy with something else.",
	codeNotReady:              "The board says the printer can't print but not why. Check the printer.",
}

// lwProblems is where a LabelWriter's fix differs.
var lwProblems = map[string]string{
	wire.CodeNotConnected: "The board can't find the printer. Check it's switched on and plugged into the board's native USB port, and that the port has 5 V.",
	wire.CodePrinterError: "The printer reports an error, such as being out of labels. Check the printer.",
}

const codeNotReady = "printer_not_ready" // the board gave no reason

func fix(family, problem string) string {
	if family == wire.FamilyLW450 {
		if msg, ok := lwProblems[problem]; ok {
			return msg
		}
	}
	if msg, ok := problems[problem]; ok {
		return msg
	}
	return "The printer can't print right now (" + problem + ")."
}

// PrinterStatus is what the server knows of a printer from its board's polls.
type PrinterStatus struct {
	Name       string     `json:"name"`
	Family     string     `json:"family"`
	Online     bool       `json:"online"`
	LastPoll   *time.Time `json:"last_poll,omitempty"`
	Media      string     `json:"media,omitempty"` // the loaded media, when it can print
	MediaName  string     `json:"media_name,omitempty"`
	TapeMM     int        `json:"tape_mm,omitempty"`     // P-touch only
	WidthDots  int        `json:"width_dots,omitempty"`  // the image width the media needs; left out for tape, where any will do
	HeightDots int        `json:"height_dots,omitempty"` // the image height the media needs
	Problem    string     `json:"problem,omitempty"`     // why it can't print, e.g. editor_lite
	Fix        string     `json:"fix,omitempty"`         // how to fix Problem, for people
	WiFi       string     `json:"wifi,omitempty"`        // the board's Wi-Fi network at LastPoll
}

// Status is every printer's status, in the order they're configured.
func (s *Service) Status() []PrinterStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]PrinterStatus, len(s.printers))
	for i, p := range s.printers {
		all[i] = s.status(p)
	}
	return all
}

// PrinterStatus is the status of the printer called name, or of the only one if
// name is "".
func (s *Service) PrinterStatus(name string) (PrinterStatus, error) {
	p, err := s.printer(name)
	if err != nil {
		return PrinterStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status(p), nil
}

// status needs s.mu held.
func (s *Service) status(p *printerState) PrinterStatus {
	st := PrinterStatus{Name: p.Name, Family: p.Family}
	if p.lastPoll.IsZero() {
		return st
	}
	// The Wi-Fi network is kept while offline: where the board was is a clue to why.
	last := p.lastPoll.UTC()
	st.LastPoll, st.WiFi, st.Online = &last, p.wifi, s.now().Sub(last) <= OfflineAfter
	if !st.Online {
		return st
	}
	if p.problem != "" {
		st.Problem, st.Fix = p.problem, fix(p.Family, p.problem)
		return st
	}
	st.Media, st.MediaName, st.TapeMM = p.media.ID, p.media.Name, p.media.TapeMM
	st.WidthDots, st.HeightDots = p.media.ImageSize()
	return st
}

// printer finds a printer by name. The name may be left out when there's only
// one; it's never guessed, as a label from the wrong printer can't be taken back.
func (s *Service) printer(name string) (*printerState, error) {
	if name == "" && len(s.printers) == 1 {
		return s.printers[0], nil
	}
	names := make([]string, len(s.printers))
	for i, p := range s.printers {
		if p.Name == name {
			return p, nil
		}
		names[i] = p.Name
	}
	if name == "" {
		return nil, refuse(http.StatusBadRequest, "printer_required", "say which printer: %s", strings.Join(names, ", "))
	}
	return nil, refuse(http.StatusBadRequest, "unknown_printer", "no printer %q; the printers are: %s", name, strings.Join(names, ", "))
}

// boardTokens is who may call the board API: each printer's token, by its name.
func (s *Service) boardTokens() Tokens {
	t := Tokens{}
	for _, p := range s.printers {
		t[p.TokenHash] = p.Name
	}
	return t
}
