package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/RolandWarburton/ptouch-print-server/internal/proto"
	"github.com/RolandWarburton/ptouch-print-server/internal/ptouch"
)

const testKey = "0123456789abcdef"

var tape24, _ = ptouch.Tape(24)

// fakePrinter has 24 mm tape loaded. Print renders the label and returns err.
// If block is set, Print waits for it to close first.
type fakePrinter struct {
	statusErr error
	printErr  error
	block     chan struct{}
	gotWantMM int
	rendered  image.Image
}

func (f *fakePrinter) Status(context.Context) (ptouch.Status, error) {
	return ptouch.Status{TapeMM: 24, MediaType: 1}, f.statusErr
}

func (f *fakePrinter) Print(_ context.Context, wantMM int, render ptouch.RenderFunc) (ptouch.Result, error) {
	if f.block != nil {
		<-f.block
	}
	f.gotWantMM = wantMM
	if f.printErr != nil {
		return ptouch.Result{}, f.printErr
	}
	img, err := render(tape24)
	if err != nil {
		return ptouch.Result{}, err
	}
	f.rendered = img
	return ptouch.Result{Tape: tape24, LengthDots: img.Bounds().Dx()}, nil
}

type response struct {
	id   byte
	body map[string]any
}

// client drives a Conn the way a Bluetooth client would, with the smallest
// chunk size so that every request is split up.
type client struct {
	t     *testing.T
	svc   *Service
	conn  *Conn
	resps chan response
	next  byte
}

func connect(t *testing.T, p Printer) *client {
	t.Helper()
	c := &client{t: t, resps: make(chan response, 10)}
	c.svc = New(p, testKey, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.conn = c.svc.Connect("test", func(id byte, body []byte) {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Errorf("response isn't JSON: %q", body)
		}
		c.resps <- response{id, m}
	})
	t.Cleanup(func() {
		c.conn.Close()
		c.svc.Wait()
	})
	return c
}

// authedClient is a connection that has already authenticated.
func authedClient(t *testing.T, p Printer) *client {
	t.Helper()
	c := connect(t, p)
	if body := c.call(proto.OpAuth, AuthMAC(testKey, c.nonce())); body["ok"] != true {
		t.Fatalf("auth failed: %v", body)
	}
	return c
}

func (c *client) info() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(c.conn.Info(proto.MinChunk), &m); err != nil {
		c.t.Fatal(err)
	}
	return m
}

func (c *client) nonce() []byte {
	n, err := hex.DecodeString(c.info()["nonce"].(string))
	if err != nil {
		c.t.Fatal(err)
	}
	return n
}

// send writes a request and returns its id without waiting for the answer.
func (c *client) send(op proto.Op, payload []byte) byte {
	c.t.Helper()
	c.next++
	for _, chunk := range proto.EncodeRequest(c.next, op, payload, proto.MinChunk) {
		if err := c.conn.Write(chunk); err != nil {
			c.t.Fatalf("write: %v", err)
		}
	}
	return c.next
}

func (c *client) wait(id byte) map[string]any {
	c.t.Helper()
	select {
	case r := <-c.resps:
		if r.id != id {
			c.t.Fatalf("response to request %d, want %d", r.id, id)
		}
		return r.body
	case <-time.After(5 * time.Second):
		c.t.Fatal("no response")
		return nil
	}
}

func (c *client) call(op proto.Op, payload []byte) map[string]any {
	c.t.Helper()
	return c.wait(c.send(op, payload))
}

func pngPayload(t *testing.T, tapeMM byte, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteByte(tapeMM)
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestInfo(t *testing.T) {
	c := connect(t, &fakePrinter{})
	info := c.info()
	if info["proto"] != 1.0 || info["chunk"] != 20.0 || info["max_msg"] != float64(proto.MaxMessage) ||
		info["authenticated"] != false || len(c.nonce()) != nonceLen {
		t.Errorf("info %v", info)
	}
	if other := connect(t, &fakePrinter{}); bytes.Equal(other.nonce(), c.nonce()) {
		t.Error("two connections got the same nonce")
	}
}

func TestAuth(t *testing.T) {
	c := connect(t, &fakePrinter{})
	if body := c.call(proto.OpStatus, nil); body["code"] != "unauthorized" {
		t.Errorf("status before auth: %v", body)
	}
	other := connect(t, &fakePrinter{})
	for name, mac := range map[string][]byte{
		"empty":                nil,
		"raw key":              []byte(testKey),
		"wrong key":            AuthMAC("wrong key 0123456", c.nonce()),
		"another conn's nonce": AuthMAC(testKey, other.nonce()),
		"truncated":            AuthMAC(testKey, c.nonce())[:16],
	} {
		if body := c.call(proto.OpAuth, mac); body["code"] != "unauthorized" {
			t.Errorf("%s: %v", name, body)
		}
		c.conn.failures = 0 // don't hit the attempt limit here
	}
	if body := c.call(proto.OpAuth, AuthMAC(testKey, c.nonce())); body["ok"] != true || body["authenticated"] != true {
		t.Errorf("right MAC: %v", body)
	}
	if c.info()["authenticated"] != true {
		t.Error("info doesn't say the connection is authenticated")
	}
	if body := c.call(proto.OpStatus, nil); body["ok"] != true {
		t.Errorf("status after auth: %v", body)
	}
}

func TestAuthAttemptLimit(t *testing.T) {
	c := connect(t, &fakePrinter{})
	for range maxAuthFailures {
		c.call(proto.OpAuth, []byte("guess"))
	}
	if body := c.call(proto.OpAuth, AuthMAC(testKey, c.nonce())); body["code"] != "unauthorized" {
		t.Errorf("right MAC after %d failures: %v", maxAuthFailures, body)
	}
}

func TestFramingErrorsFailTheWrite(t *testing.T) {
	c := connect(t, &fakePrinter{})
	if err := c.conn.Write([]byte{1, 0, 'x'}); !errors.Is(err, proto.ErrNoRequest) {
		t.Errorf("stray continuation: %v", err)
	}
}

func TestUnknownOp(t *testing.T) {
	c := authedClient(t, &fakePrinter{})
	if body := c.call(proto.Op(0x7f), nil); body["code"] != "unknown_op" {
		t.Errorf("%v", body)
	}
}

func TestOneRequestAtATime(t *testing.T) {
	p := &fakePrinter{block: make(chan struct{})}
	c := authedClient(t, p)
	first := c.send(proto.OpBarcode, []byte(`{"text": "ABC"}`))
	if body := c.call(proto.OpStatus, nil); body["code"] != "busy" {
		t.Errorf("second request while the first runs: %v", body)
	}
	close(p.block)
	if body := c.wait(first); body["printed"] != true {
		t.Errorf("first request: %v", body)
	}
}

func TestPrinterStatus(t *testing.T) {
	body := authedClient(t, &fakePrinter{}).call(proto.OpStatus, nil)
	if body["ok"] != true || body["tape_mm"] != 24.0 || body["height_dots"] != 128.0 ||
		body["supported"] != true || body["dpi"] != 180.0 {
		t.Errorf("body %v", body)
	}
	if errs, ok := body["errors"].([]any); !ok || len(errs) != 0 {
		t.Errorf("errors should be an empty list, got %v", body["errors"])
	}
}

func TestPrinterNotConnected(t *testing.T) {
	body := authedClient(t, &fakePrinter{statusErr: ptouch.ErrNotConnected}).call(proto.OpStatus, nil)
	if body["ok"] != false || body["code"] != "not_connected" {
		t.Errorf("body %v", body)
	}
}

func TestPrintBarcode(t *testing.T) {
	p := &fakePrinter{}
	body := authedClient(t, p).call(proto.OpBarcode, []byte(`{"text": "F2LXK0ABHG7F"}`))
	if body["ok"] != true || body["printed"] != true || body["tape_mm"] != 24.0 {
		t.Fatalf("body %v", body)
	}
	if p.gotWantMM != 0 {
		t.Errorf("tape_mm not given, but printer asked for %d mm", p.gotWantMM)
	}
	if p.rendered.Bounds().Dy() != 128 {
		t.Errorf("rendered %d dots high, want 128", p.rendered.Bounds().Dy())
	}
}

func TestPrintBarcodePassesTape(t *testing.T) {
	p := &fakePrinter{printErr: &ptouch.TapeMismatchError{Want: 12, Loaded: 24}}
	body := authedClient(t, p).call(proto.OpBarcode, []byte(`{"text": "ABC", "tape_mm": 12}`))
	if p.gotWantMM != 12 {
		t.Errorf("printer asked for %d mm, want 12", p.gotWantMM)
	}
	if body["code"] != "tape_mismatch" || body["loaded_tape_mm"] != 24.0 {
		t.Errorf("body %v", body)
	}
}

func TestPrintBarcodeBadRequests(t *testing.T) {
	for _, req := range []struct{ body, code string }{
		{`{"text": ""}`, "bad_text"},
		{`{"text": "café"}`, "bad_text"},
		{`{"text": "` + strings.Repeat("A", 81) + `"}`, "bad_text"},
		{`{"text": "ABC", "copies": 2}`, "bad_request"},
		{`{"text": "ABC", "tape_mm": -1}`, "bad_request"},
		{`not json`, "bad_request"},
	} {
		p := &fakePrinter{}
		if body := authedClient(t, p).call(proto.OpBarcode, []byte(req.body)); body["code"] != req.code || p.rendered != nil {
			t.Errorf("%s: %v, want %s and nothing printed", req.body, body, req.code)
		}
	}
}

func TestPrintImage(t *testing.T) {
	p := &fakePrinter{}
	body := authedClient(t, p).call(proto.OpImage, pngPayload(t, 24, 300, 128))
	if body["ok"] != true || body["length_dots"] != 300.0 || p.gotWantMM != 24 {
		t.Errorf("body %v, asked for %d mm", body, p.gotWantMM)
	}
}

func TestPrintImageBadRequests(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		code    string
	}{
		{"empty", nil, "bad_request"},
		{"tape 0", pngPayload(t, 0, 10, 128), "bad_request"},
		{"not a PNG", append([]byte{24}, "GIF89a"...), "bad_image"},
		{"too tall to be a label", pngPayload(t, 24, 10, 129), "bad_image"},
		{"too wide to decode", pngPayload(t, 24, maxImageWidth+1, 1), "bad_image"},
	}
	for _, tt := range tests {
		p := &fakePrinter{}
		if body := authedClient(t, p).call(proto.OpImage, tt.payload); body["code"] != tt.code || p.rendered != nil {
			t.Errorf("%s: %v, want %s and nothing printed", tt.name, body, tt.code)
		}
	}
}

func TestPrintErrors(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{ptouch.ErrBusy, "busy"},
		{ptouch.ErrUnconfirmed, "unconfirmed"},
		{ptouch.ErrEditorLite, "editor_lite"},
		{ptouch.ErrNoReply, "no_reply"},
		{&ptouch.PrinterError{Errors: []string{"cover open"}}, "printer_error"},
		{&ptouch.PrinterError{Errors: []string{"tape ran out"}, AfterSend: true}, "print_failed"},
		{&ptouch.UnsupportedTapeError{MM: 18}, "unsupported_tape"},
		{&ptouch.ImageHeightError{Got: 70, Want: 128}, "wrong_height"},
		{&ptouch.LabelTooLongError{Got: 5000, Max: 3600}, "too_long"},
		{ptouch.ErrEmptyLabel, "empty_label"},
		{ptouch.ErrPermission, "permission"},
		{context.Canceled, "canceled"},
		{errors.New("something else"), "internal"},
	}
	for _, tt := range tests {
		body := authedClient(t, &fakePrinter{printErr: tt.err}).call(proto.OpBarcode, []byte(`{"text": "ABC"}`))
		if body["ok"] != false || body["code"] != tt.code {
			t.Errorf("%v: code %v, want %s", tt.err, body["code"], tt.code)
		}
	}

	body := authedClient(t, &fakePrinter{printErr: ptouch.ErrUnconfirmed}).call(proto.OpBarcode, []byte(`{"text": "ABC"}`))
	if body["may_have_printed"] != true {
		t.Errorf("unconfirmed print should say it may have printed: %v", body)
	}
	body = authedClient(t, &fakePrinter{printErr: &ptouch.PrinterError{Errors: []string{"cover open"}}}).
		call(proto.OpBarcode, []byte(`{"text": "ABC"}`))
	if errs, _ := body["printer_errors"].([]any); len(errs) != 1 || errs[0] != "cover open" {
		t.Errorf("printer errors not passed on: %v", body)
	}
}

// The same vector is checked against the web client in web/src/protocol_test.ts.
func TestAuthMACVector(t *testing.T) {
	nonce := make([]byte, 16)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	const want = "3495567ceba6ac03818bcf1b28e904fbe5899e21ae2245b03d92bcaa14b71d27"
	if got := hex.EncodeToString(AuthMAC(testKey, nonce)); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
