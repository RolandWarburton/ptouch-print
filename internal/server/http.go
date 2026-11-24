package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

// Options configures the handler's auth.
type Options struct {
	ClientTokens Tokens
	Dex          *DexConfig // nil: no Dex, client tokens only
	WebUI        bool       // serve the web UI; needs Dex, which signs people in
	WebSecret    string     // the Dex client ptouch-web's secret
}

// NewHandler serves every route:
//
//	GET  /health                                         unauthenticated, for container health checks
//	GET  /.well-known/oauth-protected-resource[/mcp]     with Dex: tells claude.ai to sign in there
//	GET  /board/next?media=ID[&problem=CODE][&wifi=NAME] board: take its next job (200) or none (204)
//	POST /board/jobs/{id}/result                         board: report a job's result
//	POST /api/jobs                                       client: submit a job
//	GET  /api/jobs[?limit=N], /api/jobs/{id}             client: newest jobs, one job
//	GET  /api/status                                     client: each printer: is it online, what's loaded
//	POST /mcp                                            client: MCP Streamable HTTP, stateless
//
// A board's token says which printer it is. A P-touch board may send tape_mm=N
// in place of media.
//
// With WebUI, also the page at / and /static/, and sign-in at /auth/*; /api/*
// then accepts a signed-in browser's session cookie too.
func NewHandler(svc *Service, opts Options, log *slog.Logger) (http.Handler, error) {
	if len(svc.printers) == 0 {
		return nil, errors.New("no printers: set PRINT_PRINTERS or PTOUCH_BOARD_TOKEN_SHA256 (see ptouch-command-server token)")
	}
	board := svc.boardTokens()
	client := clientAuth{tokens: opts.ClientTokens}
	var challenge func(string) string
	mux := http.NewServeMux()

	if opts.Dex != nil {
		dex, err := newDexAuth(*opts.Dex)
		if err != nil {
			return nil, err
		}
		client.dex, challenge = dex, dex.challenge
		meta := func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, dex.metadata()) }
		mux.HandleFunc("GET /.well-known/oauth-protected-resource", meta)
		mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", meta)
	}
	if len(opts.ClientTokens) == 0 && opts.Dex == nil {
		log.Warn("no client tokens and no Dex: nobody can submit jobs")
	}
	var web *webUI
	if opts.WebUI {
		if client.dex == nil {
			return nil, errors.New("WEB_UI needs Dex to sign people in: set MCP_RESOURCE_URL, MCP_OIDC_ISSUER and MCP_ALLOWED_EMAILS")
		}
		var err error
		if web, err = newWebUI(client.dex, opts.WebSecret); err != nil {
			return nil, err
		}
		web.routes(mux)
	}
	h := handlers{svc}
	boardOnly := func(f http.HandlerFunc) http.Handler { return requireAuth(board, nil, f) }
	clientOnly := func(f http.Handler) http.Handler { return requireAuth(client, challenge, f) }
	api := func(f http.HandlerFunc) http.Handler {
		if web == nil {
			return clientOnly(f)
		}
		return web.orSession(clientOnly(f), f)
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.Handle("GET "+wire.NextPath, boardOnly(h.next))
	mux.Handle("POST /board/jobs/{id}/result", boardOnly(h.result))
	mux.Handle("POST /api/jobs", api(h.submit))
	mux.Handle("GET /api/jobs", api(h.list))
	mux.Handle("GET /api/jobs/{id}", api(h.get))
	mux.Handle("GET /api/status", api(h.status))
	mcpServer := NewMCP(svc)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux.Handle("/mcp", clientOnly(mcpHandler))
	return mux, nil
}

type handlers struct{ svc *Service }

// problemCode is the shape of the board's reason for not printing. The server
// shows it, so it accepts codes it doesn't know rather than breaking on a newer board.
var problemCode = regexp.MustCompile(`^[a-z_]{1,32}$`)

// wifiName is the board's Wi-Fi network name if it looks like one (at most 32
// bytes of printable UTF-8), else "". It's only shown, so a strange name is
// dropped rather than failing the poll.
func wifiName(s string) string {
	if len(s) > 32 || !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return ""
	}
	return s
}

func (h handlers) next(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	media, err := loadedMedia(q)
	if err != nil {
		writeError(w, err)
		return
	}
	problem := q.Get(wire.ProblemParam)
	if problem != "" && !problemCode.MatchString(problem) {
		writeError(w, refuse(http.StatusBadRequest, "bad_request", "problem must be a lower-case code like editor_lite"))
		return
	}
	job, err := h.svc.Next(r.Context(), boardPrinter(r), Poll{Media: media, Problem: problem, WiFi: wifiName(q.Get(wire.WiFiParam))})
	switch {
	case err != nil:
		writeError(w, err)
	case job == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusOK, job)
	}
}

// mediaID is the shape of a media id.
var mediaID = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// loadedMedia is the id of the media a poll says is loaded, or "" for none. A
// P-touch board may give its tape's width instead.
func loadedMedia(q url.Values) (string, error) {
	if q.Has(wire.MediaParam) {
		id := q.Get(wire.MediaParam)
		if id != "" && !mediaID.MatchString(id) {
			return "", refuse(http.StatusBadRequest, "bad_request", "media must be an id like large-address, or empty")
		}
		return id, nil
	}
	mm, err := strconv.Atoi(q.Get(wire.TapeParam))
	if err != nil || mm < 0 {
		return "", refuse(http.StatusBadRequest, "bad_request", "media must be an id, or tape_mm a number of mm or 0")
	}
	if mm == 0 {
		return "", nil
	}
	return "tape-" + strconv.Itoa(mm), nil
}

// boardPrinter is the printer whose token a board request was made with.
func boardPrinter(r *http.Request) string {
	return strings.TrimPrefix(Submitter(r.Context()), tokenPrefix)
}

func (h handlers) result(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var res wire.Result
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&res); err != nil {
		writeError(w, refuse(http.StatusBadRequest, "bad_request", "body must be a result"))
		return
	}
	if err := h.svc.Report(r.Context(), boardPrinter(r), id, res); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// submit takes {"kind": "barcode", "text": "..."},
// {"kind": "text", "lines": ["...", "..."], "size": "large", "align": "left"} or
// {"kind": "image", "tape_mm": 24, "image": "<base64 PNG>"}, each with "printer"
// when there are several, and "media" to insist on what's loaded. A LabelWriter
// takes "orientation" on the first two, and no tape_mm.
func (h handlers) submit(w http.ResponseWriter, r *http.Request) {
	var job wire.Job
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, wire.MaxImageBytes*4/3+4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&job); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, refuse(http.StatusRequestEntityTooLarge, "too_large", "image is over %d bytes", wire.MaxImageBytes))
			return
		}
		writeError(w, refuse(http.StatusBadRequest, "bad_request", "body must be a job: %v", err))
		return
	}
	j, err := h.svc.Submit(r.Context(), Submitter(r.Context()), job)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, j)
}

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		var err error
		if limit, err = strconv.Atoi(s); err != nil {
			writeError(w, refuse(http.StatusBadRequest, "bad_request", "limit must be a number"))
			return
		}
	}
	jobs, err := h.svc.List(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	j, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (h handlers) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"printers": h.svc.Status()})
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, refuse(http.StatusBadRequest, "bad_request", "job id must be a number"))
	}
	return id, err == nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError answers with an *Error's status and code, or a 500.
func writeError(w http.ResponseWriter, err error) {
	e, ok := errors.AsType[*Error](err)
	if !ok {
		e = &Error{Status: http.StatusInternalServerError, Code: "internal", Msg: "internal error"}
	}
	writeJSON(w, e.Status, e)
}
