// Package api answers requests from Bluetooth clients: authentication, printer
// status and printing. Package gatt carries the chunks to and from BlueZ; this
// package decides what they mean.
package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/RolandWarburton/ptouch-print-server/internal/label"
	"github.com/RolandWarburton/ptouch-print-server/internal/proto"
	"github.com/RolandWarburton/ptouch-print-server/internal/ptouch"
)

const (
	maxImageWidth   = 20000 // decoding limit; the printer enforces max_label_dots
	maxTextLen      = 80
	nonceLen        = 16
	maxAuthFailures = 3
)

// authContext is prefixed to the nonce before signing, so a MAC made with the
// API key can't be mistaken for one made for some other purpose.
var authContext = []byte("ptouch-auth-v1")

// AuthMAC is what a client sends to authenticate: HMAC-SHA256 keyed with the
// API key, over authContext followed by the connection's nonce.
func AuthMAC(apiKey string, nonce []byte) []byte {
	m := hmac.New(sha256.New, []byte(apiKey))
	m.Write(authContext)
	m.Write(nonce)
	return m.Sum(nil)
}

// Printer is the part of *ptouch.Printer the API uses.
type Printer interface {
	Status(ctx context.Context) (ptouch.Status, error)
	Print(ctx context.Context, wantMM int, render ptouch.RenderFunc) (ptouch.Result, error)
}

type Service struct {
	printer Printer
	apiKey  string
	log     *slog.Logger
	wg      sync.WaitGroup
}

func New(p Printer, apiKey string, log *slog.Logger) *Service {
	return &Service{printer: p, apiKey: apiKey, log: log}
}

// Wait blocks until every request that has started has been answered. Stop
// taking writes before calling it.
func (s *Service) Wait() { s.wg.Wait() }

// Conn is one connected client. Each connection gets its own nonce and must
// authenticate before anything but the auth op is allowed.
type Conn struct {
	s       *Service
	peer    string
	nonce   []byte
	respond func(id byte, body []byte)
	ctx     context.Context
	cancel  context.CancelFunc

	mu       sync.Mutex
	reasm    proto.Reassembler
	authed   bool
	failures int
	busy     bool
}

// Connect starts a connection for peer, a name used in logs. respond is
// called with each complete response body, which the transport splits into
// indications; it may be called from any goroutine.
func (s *Service) Connect(peer string, respond func(id byte, body []byte)) *Conn {
	nonce := make([]byte, nonceLen)
	rand.Read(nonce)
	ctx, cancel := context.WithCancel(context.Background())
	return &Conn{s: s, peer: peer, nonce: nonce, respond: respond, ctx: ctx, cancel: cancel}
}

// Close ends the connection. A request still running is canceled unless its
// label has already been sent to the printer.
func (c *Conn) Close() { c.cancel() }

type infoResponse struct {
	Proto         int    `json:"proto"`
	Nonce         string `json:"nonce"`
	Chunk         int    `json:"chunk"`
	MaxMsg        int    `json:"max_msg"`
	Authenticated bool   `json:"authenticated"`
}

// Info is the value of the info characteristic. chunk is the largest
// attribute value the connection's MTU allows.
func (c *Conn) Info(chunk int) []byte {
	c.mu.Lock()
	authed := c.authed
	c.mu.Unlock()
	b, _ := json.Marshal(infoResponse{
		Proto: proto.Version, Nonce: hex.EncodeToString(c.nonce), Chunk: chunk,
		MaxMsg: proto.MaxMessage, Authenticated: authed,
	})
	return b
}

// Write takes one chunk written to the request characteristic. It returns an
// error only for broken framing, which the transport reports as a failed
// write. Everything else is answered through respond.
func (c *Conn) Write(chunk []byte) error {
	c.mu.Lock()
	req, err := c.reasm.Add(chunk, time.Now())
	if err != nil || req == nil {
		c.mu.Unlock()
		return err
	}
	if c.busy {
		c.mu.Unlock()
		c.send(req.ID, errorResponse{Error: "the previous request on this connection hasn't been answered yet", Code: "busy"})
		return nil
	}
	c.busy = true
	c.mu.Unlock()

	c.s.wg.Go(func() {
		resp := c.handle(req)
		c.mu.Lock()
		c.busy = false
		c.mu.Unlock()
		c.send(req.ID, resp)
	})
	return nil
}

func (c *Conn) send(id byte, v any) {
	b, _ := json.Marshal(v)
	c.respond(id, b)
}

func (c *Conn) handle(req *proto.Request) any {
	if req.Op == proto.OpAuth {
		return c.auth(req.Payload)
	}
	c.mu.Lock()
	authed := c.authed
	c.mu.Unlock()
	if !authed {
		return errorResponse{Error: "authenticate first", Code: "unauthorized"}
	}
	switch req.Op {
	case proto.OpStatus:
		return c.status()
	case proto.OpBarcode:
		return c.printBarcode(req.Payload)
	case proto.OpImage:
		return c.printImage(req.Payload)
	}
	return badRequest("unknown_op", fmt.Sprintf("unknown %v", req.Op))
}

type authResponse struct {
	OK            bool `json:"ok"`
	Authenticated bool `json:"authenticated"`
}

func (c *Conn) auth(mac []byte) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failures >= maxAuthFailures {
		return errorResponse{Error: "too many failed attempts, reconnect to try again", Code: "unauthorized"}
	}
	if !hmac.Equal(mac, AuthMAC(c.s.apiKey, c.nonce)) {
		c.failures++
		c.s.log.Warn("authentication failed", "peer", c.peer, "failures", c.failures)
		return errorResponse{Error: "wrong API key", Code: "unauthorized"}
	}
	c.authed = true
	return authResponse{OK: true, Authenticated: true}
}

type printerResponse struct {
	OK               bool     `json:"ok"`
	TapeMM           int      `json:"tape_mm"`
	Supported        bool     `json:"supported"`
	HeightDots       int      `json:"height_dots,omitempty"`
	GeometryVerified bool     `json:"geometry_verified,omitempty"`
	MediaType        byte     `json:"media_type"`
	DPI              int      `json:"dpi"`
	Errors           []string `json:"errors"`
}

func (c *Conn) status() any {
	st, err := c.s.printer.Status(c.ctx)
	if err != nil {
		return c.fail(proto.OpStatus, err)
	}
	resp := printerResponse{OK: true, TapeMM: st.TapeMM, MediaType: st.MediaType, DPI: ptouch.DPI, Errors: st.Errors}
	if resp.Errors == nil {
		resp.Errors = []string{}
	}
	if t, ok := ptouch.Tape(st.TapeMM); ok {
		resp.Supported, resp.HeightDots, resp.GeometryVerified = true, t.Dots, t.Verified
	}
	return resp
}

type printResponse struct {
	OK         bool    `json:"ok"`
	Printed    bool    `json:"printed"`
	TapeMM     int     `json:"tape_mm"`
	LengthDots int     `json:"length_dots"`
	LengthMM   float64 `json:"length_mm"`
}

// printImage prints a PNG the client rendered for a specific tape width. The
// payload is one byte of tape width in mm followed by the PNG.
func (c *Conn) printImage(payload []byte) any {
	if len(payload) < 1 || payload[0] == 0 {
		return badRequest("bad_request", "payload must be the tape width in mm (one byte, not 0) followed by a PNG")
	}
	img, err := decodePNG(bytes.NewReader(payload[1:]))
	if err != nil {
		return badRequest("bad_image", err.Error())
	}
	return c.print(proto.OpImage, int(payload[0]), func(ptouch.TapeSpec) (image.Image, error) { return img, nil })
}

// decodePNG checks the dimensions before decoding, so a small file can't
// expand into a huge image.
func decodePNG(f io.ReadSeeker) (image.Image, error) {
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return nil, errors.New("image must be a PNG")
	}
	if cfg.Height > ptouch.HeadPins || cfg.Width > maxImageWidth {
		return nil, fmt.Errorf("image is %dx%d, too large for a label", cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, err := png.Decode(f)
	if err != nil {
		return nil, errors.New("image must be a PNG")
	}
	return img, nil
}

type barcodeRequest struct {
	Text   string `json:"text"`
	TapeMM int    `json:"tape_mm,omitempty"` // optional; rejected if it isn't what's loaded
}

// printBarcode draws a barcode label for whatever tape is loaded.
func (c *Conn) printBarcode(payload []byte) any {
	var req barcodeRequest
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return badRequest("bad_request", "expected JSON {\"text\": \"...\", \"tape_mm\": optional}")
	}
	if msg := validateText(req.Text); msg != "" {
		return badRequest("bad_text", msg)
	}
	if req.TapeMM < 0 {
		return badRequest("bad_request", "tape_mm must be positive")
	}
	return c.print(proto.OpBarcode, req.TapeMM, func(t ptouch.TapeSpec) (image.Image, error) {
		return label.Barcode(req.Text, t.Dots)
	})
}

func validateText(text string) string {
	if text == "" || len(text) > maxTextLen {
		return fmt.Sprintf("text must be 1 to %d characters", maxTextLen)
	}
	for _, c := range []byte(text) {
		if c < 0x20 || c > 0x7e {
			return "text must be printable ASCII"
		}
	}
	return ""
}

func (c *Conn) print(op proto.Op, tapeMM int, render ptouch.RenderFunc) any {
	res, err := c.s.printer.Print(c.ctx, tapeMM, render)
	if err != nil {
		return c.fail(op, err)
	}
	c.s.log.Info("printed", "op", op, "tape_mm", res.Tape.MM, "length_dots", res.LengthDots, "peer", c.peer)
	return printResponse{
		OK:         true,
		Printed:    true,
		TapeMM:     res.Tape.MM,
		LengthDots: res.LengthDots,
		LengthMM:   float64(res.LengthDots) * 25.4 / ptouch.DPI,
	}
}

type errorResponse struct {
	OK             bool     `json:"ok"` // always false
	Error          string   `json:"error"`
	Code           string   `json:"code"`
	PrinterErrors  []string `json:"printer_errors,omitempty"`
	LoadedTapeMM   int      `json:"loaded_tape_mm,omitempty"`
	MayHavePrinted bool     `json:"may_have_printed,omitempty"`
}

func badRequest(code, msg string) errorResponse {
	return errorResponse{Error: msg, Code: code}
}

// fail turns a printer error into a response and logs it.
func (c *Conn) fail(op proto.Op, err error) errorResponse {
	resp := classify(err)
	c.s.log.Warn("request failed", "op", op, "code", resp.Code, "err", err, "peer", c.peer)
	return resp
}

func classify(err error) errorResponse {
	resp := errorResponse{Error: err.Error()}
	if pe, ok := errors.AsType[*ptouch.PrinterError](err); ok {
		resp.PrinterErrors = pe.Errors
		if pe.AfterSend {
			resp.Code = "print_failed"
		} else {
			resp.Code = "printer_error"
		}
		return resp
	}
	if ue, ok := errors.AsType[*ptouch.UnsupportedTapeError](err); ok {
		resp.Code, resp.LoadedTapeMM = "unsupported_tape", ue.MM
		return resp
	}
	if me, ok := errors.AsType[*ptouch.TapeMismatchError](err); ok {
		resp.Code, resp.LoadedTapeMM = "tape_mismatch", me.Loaded
		return resp
	}
	if _, ok := errors.AsType[*ptouch.ImageHeightError](err); ok {
		resp.Code = "wrong_height"
		return resp
	}
	if _, ok := errors.AsType[*ptouch.LabelTooLongError](err); ok {
		resp.Code = "too_long"
		return resp
	}
	switch {
	case errors.Is(err, ptouch.ErrEmptyLabel):
		resp.Code = "empty_label"
	case errors.Is(err, ptouch.ErrNotConnected):
		resp.Code = "not_connected"
	case errors.Is(err, ptouch.ErrEditorLite):
		resp.Code = "editor_lite"
	case errors.Is(err, ptouch.ErrNoReply):
		resp.Code = "no_reply"
	case errors.Is(err, ptouch.ErrBusy):
		resp.Code = "busy"
	case errors.Is(err, ptouch.ErrUnconfirmed):
		// Don't retry automatically: the label may already be on the tape.
		resp.Code, resp.MayHavePrinted = "unconfirmed", true
	case errors.Is(err, context.Canceled):
		resp.Code = "canceled"
	case errors.Is(err, ptouch.ErrPermission):
		resp.Code = "permission"
	default:
		resp.Code = "internal"
	}
	return resp
}
