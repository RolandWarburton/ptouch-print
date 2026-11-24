package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

const dymoToken = "dymo-secret"

func newTwoPrinters(t *testing.T) *env {
	t.Helper()
	return newEnvPrinters(t, Options{},
		Printer{"ptouch", wire.FamilyPTouch, HashToken(boardToken)},
		Printer{"office-dymo", wire.FamilyLW450, HashToken(dymoToken)})
}

func (e *env) pollDymo(query string) (int, map[string]any) {
	e.t.Helper()
	return e.do("GET", "/board/next?"+query, dymoToken, nil)
}

func barcodeFor(printer string) map[string]any {
	return map[string]any{"kind": "barcode", "text": "TG17573", "printer": printer}
}

func TestJobsGoToTheirPrinter(t *testing.T) {
	e := newTwoPrinters(t)
	e.poll("24")
	if code, body := e.do("POST", "/api/jobs", clientToken, barcodeFor("office-dymo")); code != 503 || body["code"] != "printer_offline" {
		t.Fatalf("one printer polling made the other online: %d %v", code, body)
	}
	e.pollDymo("media=large-address")

	if code, body := e.do("POST", "/api/jobs", clientToken, map[string]any{"kind": "barcode", "text": "A"}); code != 400 ||
		body["code"] != "printer_required" || !strings.Contains(body["error"].(string), "ptouch, office-dymo") {
		t.Errorf("no printer named: %d %v", code, body)
	}
	if code, body := e.do("POST", "/api/jobs", clientToken, barcodeFor("nope")); code != 400 || body["code"] != "unknown_printer" {
		t.Errorf("unknown printer: %d %v", code, body)
	}
	code, job := e.do("POST", "/api/jobs", clientToken, barcodeFor("office-dymo"))
	if code != 201 || job["printer"] != "office-dymo" || job["media"] != "large-address" || job["orientation"] != "portrait" {
		t.Fatalf("submit: %d %v", code, job)
	}

	if code, _ := e.poll("24"); code != 204 {
		t.Fatalf("the P-touch got the LabelWriter's job: %d", code)
	}
	code, sent := e.pollDymo("media=large-address")
	if code != 200 || sent["text"] != "TG17573" || sent["media"] != "large-address" || sent["orientation"] != "portrait" {
		t.Fatalf("poll: %d %v", code, sent)
	}
	if code, body := e.do("POST", wire.ResultPath(1), boardToken, wire.Result{Printed: true}); code != 404 {
		t.Errorf("the P-touch reported the LabelWriter's job: %d %v", code, body)
	}
	if code, _ := e.do("POST", wire.ResultPath(1), dymoToken, wire.Result{Printed: true}); code != 204 {
		t.Errorf("report: %d", code)
	}
	if code, _ := e.do("GET", "/api/status", dymoToken, nil); code != 401 {
		t.Errorf("a board token on the client API: %d", code)
	}
}

func TestStatusPerPrinter(t *testing.T) {
	e := newTwoPrinters(t)
	e.poll("24&wifi=Home")
	e.pollDymo("media=large-address&wifi=Office")
	printers := func() (ptouch, dymo map[string]any) {
		_, body := e.do("GET", "/api/status", clientToken, nil)
		all := body["printers"].([]any)
		if len(all) != 2 {
			t.Fatalf("status %v", body)
		}
		return all[0].(map[string]any), all[1].(map[string]any)
	}
	p, d := printers()
	if p["name"] != "ptouch" || p["family"] != "ptouch" || p["media"] != "tape-24" || p["tape_mm"] != 24.0 ||
		p["height_dots"] != 128.0 || p["width_dots"] != nil || p["wifi"] != "Home" {
		t.Errorf("ptouch %v", p)
	}
	if d["name"] != "office-dymo" || d["family"] != "lw450" || d["media"] != "large-address" || d["tape_mm"] != nil ||
		d["width_dots"] != 400.0 || d["height_dots"] != 950.0 || d["wifi"] != "Office" {
		t.Errorf("dymo %v", d)
	}

	// A problem, or going offline, is one printer's own.
	e.pollDymo("media=&problem=not_connected")
	p, d = printers()
	if p["problem"] != nil || d["problem"] != "not_connected" || d["media"] != nil || !strings.Contains(d["fix"].(string), "native USB") {
		t.Errorf("dymo unplugged: %v %v", p, d)
	}
	e.now = e.now.Add(OfflineAfter + time.Second)
	e.poll("24")
	if p, d = printers(); p["online"] != true || d["online"] != false {
		t.Errorf("dymo offline: %v %v", p, d)
	}
}

func TestPollMedia(t *testing.T) {
	e := newTwoPrinters(t)
	for _, tt := range []struct{ query, code string }{
		{"media=", "printer_not_ready"},
		{"media=from-newer-firmware", "unsupported_media"},
		{"media=tape-24", "unsupported_media"}, // another family's
		{"tape_mm=24", "unsupported_media"},
		{"media=large-address&problem=printer_error", "printer_error"},
	} {
		if code, _ := e.pollDymo(tt.query); code != 204 {
			t.Errorf("%s: poll %d", tt.query, code)
		}
		if code, body := e.do("POST", "/api/jobs", clientToken, barcodeFor("office-dymo")); code != 503 || body["code"] != tt.code {
			t.Errorf("%s: %d %v, want 503 %s", tt.query, code, body, tt.code)
		}
	}
	for _, query := range []string{"", "media=Not%20An%20ID", "tape_mm=x"} {
		if code, _ := e.pollDymo(query); code != 400 {
			t.Errorf("poll %q: %d", query, code)
		}
	}
	// A P-touch may say media too.
	e.do("GET", "/board/next?media=tape-12", boardToken, nil)
	if st := e.status(); st["tape_mm"] != 12.0 {
		t.Errorf("status %v", st)
	}
}

func TestLabelWriterJobs(t *testing.T) {
	e := newTwoPrinters(t)
	e.poll("24")
	e.pollDymo("media=large-address")
	nine := strings.Split("123456789", "")
	for _, tt := range []struct {
		name   string
		job    map[string]any
		status int
		code   string
	}{
		{"other media", map[string]any{"kind": "barcode", "text": "A", "media": "tape-24"}, 409, "wrong_media"},
		{"bad orientation", map[string]any{"kind": "barcode", "text": "A", "orientation": "sideways"}, 400, "bad_request"},
		{"tape_mm", map[string]any{"kind": "barcode", "text": "A", "tape_mm": 24}, 400, "bad_request"},
		{"nine lines", map[string]any{"kind": "text", "lines": nine}, 400, "bad_text"},
		{"no lines", map[string]any{"kind": "text"}, 400, "bad_text"},
		{"not ASCII", map[string]any{"kind": "text", "lines": []string{"A", "café"}}, 400, "bad_text"},
		{"long line", map[string]any{"kind": "text", "lines": []string{strings.Repeat("x", 81)}}, 400, "bad_text"},
		{"image for tape", map[string]any{"kind": "image", "image": pngSize(t, 400, 128)}, 422, "wrong_size"},
		{"image turned", map[string]any{"kind": "image", "image": pngSize(t, 950, 400)}, 422, "wrong_size"},
		{"oriented image", map[string]any{"kind": "image", "image": pngSize(t, 400, 950), "orientation": "portrait"}, 400, "bad_request"},
	} {
		tt.job["printer"] = "office-dymo"
		if code, body := e.do("POST", "/api/jobs", clientToken, tt.job); code != tt.status || body["code"] != tt.code {
			t.Errorf("%s: %d %v, want %d %s", tt.name, code, body, tt.status, tt.code)
		}
	}
	for _, job := range []map[string]any{
		{"kind": "text", "lines": nine[:8], "orientation": "landscape", "media": "large-address"},
		{"kind": "image", "image": pngSize(t, 400, 950)},
	} {
		job["printer"] = "office-dymo"
		if code, body := e.do("POST", "/api/jobs", clientToken, job); code != 201 {
			t.Errorf("good %s: %d %v", job["kind"], code, body)
		}
	}
	if _, sent := e.pollDymo("media=large-address"); len(sent["lines"].([]any)) != 8 || sent["orientation"] != "landscape" ||
		sent["size"] != "large" || sent["align"] != "left" || sent["media"] != "large-address" {
		t.Errorf("board got %v", sent)
	}

	// The P-touch is as it was: no orientation, and two lines at most.
	if code, body := e.do("POST", "/api/jobs", clientToken,
		map[string]any{"kind": "barcode", "text": "A", "printer": "ptouch", "orientation": "landscape"}); code != 400 {
		t.Errorf("orientation on a P-touch: %d %v", code, body)
	}
	if code, body := e.do("POST", "/api/jobs", clientToken,
		map[string]any{"kind": "text", "lines": []string{"A", "B", "C"}, "printer": "ptouch"}); code != 400 {
		t.Errorf("three lines on a P-touch: %d %v", code, body)
	}
	if code, body := e.do("POST", "/api/jobs", clientToken,
		map[string]any{"kind": "barcode", "text": "A", "printer": "ptouch", "media": "tape-12"}); code != 409 || body["code"] != "wrong_media" {
		t.Errorf("other tape on a P-touch: %d %v", code, body)
	}
}

func TestMCPPrinters(t *testing.T) {
	e := newTwoPrinters(t)
	e.poll("24")
	e.pollDymo("media=large-address")
	if _, body := rpc(t, e.url, clientToken, "print_barcode", map[string]any{"text": "A"}); !strings.Contains(body, "printer_required") {
		t.Errorf("no printer: %s", body)
	}
	if _, body := rpc(t, e.url, clientToken, "print_text", map[string]any{"lines": []string{"A", "B", "C"}, "printer": "office-dymo",
		"orientation": "landscape"}); !strings.Contains(body, `\"printer\":\"office-dymo\"`) || !strings.Contains(body, `\"orientation\":\"landscape\"`) {
		t.Errorf("print_text: %s", body)
	}
	if _, body := rpc(t, e.url, clientToken, "printer_status", map[string]any{}); !strings.Contains(body, `\"name\":\"ptouch\"`) ||
		!strings.Contains(body, `\"name\":\"office-dymo\"`) {
		t.Errorf("printer_status: %s", body)
	}
	if _, body := rpc(t, e.url, clientToken, "printer_status", map[string]any{"printer": "office-dymo"}); strings.Contains(body, "ptouch") ||
		!strings.Contains(body, `\"media\":\"large-address\"`) {
		t.Errorf("printer_status for one: %s", body)
	}
}

func TestParsePrinters(t *testing.T) {
	x, y := HashToken("x"), HashToken("y")
	got, err := ParsePrinters(" office-dymo:lw450:"+strings.ToUpper(y)+", ", x)
	if want := []Printer{{"ptouch", "ptouch", x}, {"office-dymo", "lw450", y}}; err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{
		"a:lw450", "a:lw450:abc", ":lw450:" + x, "a:lw550:" + x, "Office Dymo:lw450:" + x,
		"a:lw450:" + x + ",a:ptouch:" + y, "a:lw450:" + x + ",b:ptouch:" + x,
	} {
		if _, err := ParsePrinters(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParsePrinters("ptouch:ptouch:"+y, x); err == nil {
		t.Error("ptouch configured in both settings accepted")
	}
}
