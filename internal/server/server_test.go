package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RolandWarburton/ptouch-print-server/internal/store"
	"github.com/RolandWarburton/ptouch-print-server/internal/wire"
)

const (
	boardToken  = "board-secret"
	clientToken = "client-secret"
)

type env struct {
	t   *testing.T
	url string
	svc *Service
	now time.Time
}

func newEnv(t *testing.T, dex *DexConfig) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &env{t: t, svc: NewService(st, log), now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	e.svc.now = func() time.Time { return e.now }
	h, err := NewHandler(e.svc, Options{
		BoardTokenHash: HashToken(boardToken),
		ClientTokens:   Tokens{HashToken(clientToken): "ci"},
		Dex:            dex,
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e.url = srv.URL
	return e
}

// do sends a request and decodes a JSON answer into a map (nil for no body).
func (e *env) do(method, path, token string, body any) (int, map[string]any) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.url+path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var m map[string]any
	if len(b) > 0 && b[0] == '{' {
		json.Unmarshal(b, &m)
	}
	return res.StatusCode, m
}

func (e *env) poll(tape string) (int, map[string]any) {
	e.t.Helper()
	return e.do("GET", "/board/next?tape_mm="+tape, boardToken, nil)
}

func pngB64(t *testing.T, h int) string {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 50, h))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestBarcodeJobLifecycle(t *testing.T) {
	e := newEnv(t, nil)
	barcode := map[string]any{"kind": "barcode", "text": "TG17573"}

	if code, body := e.do("POST", "/api/jobs", clientToken, barcode); code != 503 || body["code"] != "printer_offline" {
		t.Fatalf("before the board polls: %d %v", code, body)
	}
	e.poll("0&problem=editor_lite")
	if code, body := e.do("POST", "/api/jobs", clientToken, barcode); code != 503 || body["code"] != "editor_lite" ||
		!strings.Contains(body["error"].(string), "Editor Lite button") {
		t.Fatalf("Editor Lite on: %d %v", code, body)
	}
	if code, _ := e.poll("24"); code != 204 {
		t.Fatalf("empty queue: %d", code)
	}
	code, job := e.do("POST", "/api/jobs", clientToken, barcode)
	if code != 201 || job["status"] != "queued" || job["submitter"] != "token:ci" {
		t.Fatalf("submit: %d %v", code, job)
	}
	id := int64(job["id"].(float64))

	code, sent := e.poll("24")
	if code != 200 || sent["text"] != "TG17573" || sent["kind"] != "barcode" {
		t.Fatalf("poll: %d %v", code, sent)
	}
	if _, j := e.do("GET", "/api/jobs/1", clientToken, nil); j["status"] != "sent" {
		t.Errorf("after the board took it: %v", j)
	}
	if code, _ := e.do("POST", wire.ResultPath(id), boardToken, wire.Result{Printed: true, TapeMM: 24}); code != 204 {
		t.Fatalf("report: %d", code)
	}
	if code, body := e.do("POST", wire.ResultPath(id), boardToken, wire.Result{}); code != 409 || body["code"] != "not_sent" {
		t.Errorf("second report: %d %v", code, body)
	}
	if code, _ := e.do("POST", wire.ResultPath(99), boardToken, wire.Result{}); code != 404 {
		t.Errorf("report for a missing job: %d", code)
	}
	if _, j := e.do("GET", "/api/jobs/1", clientToken, nil); j["status"] != "done" || j["result"] == nil {
		t.Errorf("after the report: %v", j)
	}
	if code, _ := e.poll("24"); code != 204 {
		t.Errorf("a finished job was handed out again: %d", code)
	}
}

func TestOfflineAfterMissedPolls(t *testing.T) {
	e := newEnv(t, nil)
	e.poll("12")
	e.now = e.now.Add(OfflineAfter + time.Second)
	if code, body := e.do("POST", "/api/jobs", clientToken, map[string]any{"kind": "barcode", "text": "A"}); code != 503 ||
		body["code"] != "printer_offline" {
		t.Errorf("%d %v", code, body)
	}
	if _, st := e.do("GET", "/api/status", clientToken, nil); st["online"] != false || st["tape_mm"] != 0.0 {
		t.Errorf("status %v", st)
	}
}

func TestPrinterProblems(t *testing.T) {
	e := newEnv(t, nil)
	barcode := map[string]any{"kind": "barcode", "text": "A"}
	for _, tt := range []struct{ poll, code string }{
		{"0&problem=printer_error", "printer_error"},
		{"0&problem=not_connected", "not_connected"},
		{"0&problem=from_a_newer_board", "from_a_newer_board"},
		{"0", "printer_not_ready"},
	} {
		e.poll(tt.poll)
		if code, body := e.do("POST", "/api/jobs", clientToken, barcode); code != 503 || body["code"] != tt.code {
			t.Errorf("poll %s: %d %v, want 503 %s", tt.poll, code, body, tt.code)
		}
	}
	e.poll("0&problem=editor_lite")
	if _, st := e.do("GET", "/api/status", clientToken, nil); st["problem"] != "editor_lite" || st["tape_mm"] != 0.0 {
		t.Errorf("status with a problem: %v", st)
	}
	// A poll without a problem clears it.
	e.poll("24")
	if _, st := e.do("GET", "/api/status", clientToken, nil); st["problem"] != nil || st["tape_mm"] != 24.0 {
		t.Errorf("status after the fix: %v", st)
	}
	if code, _ := e.do("POST", "/api/jobs", clientToken, barcode); code != 201 {
		t.Errorf("submit after the fix: %d", code)
	}
	if code, _ := e.poll("0&problem=Not%20A%20Code"); code != 400 {
		t.Errorf("malformed problem: %d", code)
	}
}

// A job queued before the problem is still handed out, so it fails now
// instead of printing later.
func TestQueuedJobHandedOutDespiteProblem(t *testing.T) {
	e := newEnv(t, nil)
	e.poll("24")
	e.do("POST", "/api/jobs", clientToken, map[string]any{"kind": "barcode", "text": "A"})
	if code, job := e.poll("0&problem=editor_lite"); code != 200 || job["text"] != "A" {
		t.Errorf("poll with a problem: %d %v", code, job)
	}
}

func TestImageJobs(t *testing.T) {
	e := newEnv(t, nil)
	e.poll("24")
	if _, st := e.do("GET", "/api/status", clientToken, nil); st["online"] != true || st["height_dots"] != 128.0 {
		t.Fatalf("status %v", st)
	}
	for _, tt := range []struct {
		name   string
		job    map[string]any
		status int
		code   string
	}{
		{"wrong tape", map[string]any{"kind": "image", "tape_mm": 12, "image": pngB64(t, 70)}, 409, "tape_mismatch"},
		{"wrong height", map[string]any{"kind": "image", "tape_mm": 24, "image": pngB64(t, 70)}, 422, "wrong_height"},
		{"no tape_mm", map[string]any{"kind": "image", "image": pngB64(t, 128)}, 400, "bad_request"},
		{"not a PNG", map[string]any{"kind": "image", "tape_mm": 24, "image": "R0lGODlh"}, 400, "bad_image"},
		{"too large", map[string]any{"kind": "image", "tape_mm": 24, "image": strings.Repeat("A", wire.MaxImageBytes*2)}, 413, "too_large"},
		{"bad text", map[string]any{"kind": "barcode", "text": "café"}, 400, "bad_text"},
		{"unknown kind", map[string]any{"kind": "qr", "text": "A"}, 400, "bad_request"},
		{"unknown field", map[string]any{"kind": "barcode", "text": "A", "copies": 2}, 400, "bad_request"},
	} {
		if code, body := e.do("POST", "/api/jobs", clientToken, tt.job); code != tt.status || body["code"] != tt.code {
			t.Errorf("%s: %d %v, want %d %s", tt.name, code, body, tt.status, tt.code)
		}
	}
	if code, body := e.do("POST", "/api/jobs", clientToken, map[string]any{"kind": "image", "tape_mm": 24, "image": pngB64(t, 128)}); code != 201 {
		t.Fatalf("good image: %d %v", code, body)
	}
	if _, job := e.poll("24"); job["kind"] != "image" || job["tape_mm"] != 24.0 || job["image"] == "" {
		t.Errorf("board got %v", job)
	}
}

func TestTokensAreSeparate(t *testing.T) {
	e := newEnv(t, nil)
	if code, _ := e.do("GET", "/board/next?tape_mm=24", clientToken, nil); code != 401 {
		t.Errorf("client token on the board API: %d", code)
	}
	if code, _ := e.do("GET", "/api/status", boardToken, nil); code != 401 {
		t.Errorf("board token on the client API: %d", code)
	}
	if code, _ := e.do("GET", "/api/status", "", nil); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := e.do("GET", "/board/next?tape_mm=x", boardToken, nil); code != 400 {
		t.Errorf("bad tape_mm: %d", code)
	}
	if code, _ := e.do("GET", "/health", "", nil); code != 200 {
		t.Errorf("health: %d", code)
	}
}

func rpc(t *testing.T, url, token, tool string, args any) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, url+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestMCPTools(t *testing.T) {
	e := newEnv(t, nil)
	if code, body := rpc(t, e.url, clientToken, "print_barcode", map[string]any{"text": "A"}); code != 200 ||
		!strings.Contains(body, `"isError":true`) || !strings.Contains(body, "printer_offline") {
		t.Fatalf("offline: %d %s", code, body)
	}
	e.poll("24")
	if code, body := rpc(t, e.url, clientToken, "print_barcode", map[string]any{"text": "TG17573"}); code != 200 ||
		!strings.Contains(body, `\"status\":\"queued\"`) {
		t.Fatalf("print: %d %s", code, body)
	}
	// The caller's identity reaches the tool and is stored as the submitter.
	if j, _ := e.svc.Get(t.Context(), 1); j.Submitter != "token:ci" {
		t.Errorf("submitter %q", j.Submitter)
	}
	if _, body := rpc(t, e.url, clientToken, "get_job", map[string]any{"id": 1}); !strings.Contains(body, "TG17573") {
		t.Errorf("get_job: %s", body)
	}
	if _, body := rpc(t, e.url, clientToken, "get_job", map[string]any{"id": 9}); !strings.Contains(body, "not_found") {
		t.Errorf("get_job missing: %s", body)
	}
	if _, body := rpc(t, e.url, clientToken, "list_jobs", map[string]any{}); !strings.Contains(body, "TG17573") {
		t.Errorf("list_jobs: %s", body)
	}
	if _, body := rpc(t, e.url, clientToken, "printer_status", map[string]any{}); !strings.Contains(body, `\"online\":true`) ||
		!strings.Contains(body, `\"tape_mm\":24`) {
		t.Errorf("printer_status: %s", body)
	}
	e.poll("0&problem=editor_lite")
	if _, body := rpc(t, e.url, clientToken, "printer_status", map[string]any{}); !strings.Contains(body, `\"problem\":\"editor_lite\"`) {
		t.Errorf("printer_status with a problem: %s", body)
	}
	if code, _ := rpc(t, e.url, boardToken, "list_jobs", map[string]any{}); code != 401 {
		t.Errorf("board token on /mcp: %d", code)
	}
}

// jwt builds an unsigned JWT-shaped token with the given audience.
func jwt(aud any) string {
	b, _ := json.Marshal(map[string]any{"aud": aud})
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func fakeDex(t *testing.T, emails map[string]string) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		email, ok := emails[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if r.URL.Path != "/userinfo" || !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"email": email})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestDexAuth(t *testing.T) {
	good, wrongAud, stranger := jwt("ptouch-mcp"), jwt([]string{"bookstack-mcp"}), jwt([]string{"x", "ptouch-mcp"})
	dex, calls := fakeDex(t, map[string]string{good: "Me@Example.com", wrongAud: "me@example.com", stranger: "eve@example.com"})
	e := newEnv(t, &DexConfig{ResourceURL: "https://ptouch.example.com/mcp", Issuer: dex.URL, Allowed: "me@example.com"})
	e.poll("24")

	req, _ := http.NewRequest("POST", e.url+"/mcp", strings.NewReader("{}"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 || !strings.Contains(res.Header.Get("WWW-Authenticate"),
		`resource_metadata="https://ptouch.example.com/.well-known/oauth-protected-resource"`) {
		t.Fatalf("no token: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	if code, _ := rpc(t, e.url, wrongAud, "list_jobs", map[string]any{}); code != 401 {
		t.Errorf("token for another Dex client: %d", code)
	}
	if code, _ := rpc(t, e.url, stranger, "list_jobs", map[string]any{}); code != 403 {
		t.Errorf("email not allowed: %d", code)
	}
	before := calls.Load()
	for range 2 {
		if code, body := rpc(t, e.url, good, "print_barcode", map[string]any{"text": "A"}); code != 200 || strings.Contains(body, "isError\":true") {
			t.Fatalf("allowed: %d %s", code, body)
		}
	}
	if n := calls.Load() - before; n != 1 {
		t.Errorf("userinfo called %d times for one token; want 1 (cached)", n)
	}
	if j, _ := e.svc.Get(t.Context(), 1); j.Submitter != "me@example.com" {
		t.Errorf("submitter %q", j.Submitter)
	}
	// Client tokens still work alongside Dex.
	if code, _ := e.do("GET", "/api/status", clientToken, nil); code != 200 {
		t.Errorf("client token with Dex on: %d", code)
	}
	if code, meta := e.do("GET", "/.well-known/oauth-protected-resource/mcp", "", nil); code != 200 ||
		meta["resource"] != "https://ptouch.example.com/mcp" {
		t.Errorf("metadata %d %v", code, meta)
	}
}

func TestParseTokens(t *testing.T) {
	h := HashToken("x")
	got, err := ParseTokens(" ci:" + h + ", ,ops:" + strings.ToUpper(HashToken("y")))
	if err != nil || got[h] != "ci" || got[HashToken("y")] != "ops" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"ci", "ci:abc", ":" + h, "ci:" + h + ",ci:" + HashToken("y")} {
		if _, err := ParseTokens(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBoardTokenRequired(t *testing.T) {
	if _, err := NewHandler(nil, Options{}, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Error("started without a board token")
	}
}
