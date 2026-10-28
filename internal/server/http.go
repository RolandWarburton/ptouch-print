package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

// Options configures the handler's auth.
type Options struct {
	BoardTokenHash string
	ClientTokens   Tokens
	Dex            *DexConfig // nil: no Dex, client tokens only
	WebUI          bool       // serve the web UI; needs Dex, which signs people in
	WebSecret      string     // the Dex client ptouch-web's secret
}

// NewHandler serves every route:
//
//	GET  /health                                       unauthenticated, for container health checks
//	GET  /.well-known/oauth-protected-resource[/mcp]   with Dex: tells claude.ai to sign in there
//	GET  /board/next?tape_mm=N[&problem=CODE]          board: take the next job (200) or none (204)
//	POST /board/jobs/{id}/result                       board: report a job's result
//	POST /api/jobs                                     client: submit a job
//	GET  /api/jobs[?limit=N], /api/jobs/{id}           client: newest jobs, one job
//	GET  /api/status                                   client: is the board online, which tape
//	POST /mcp                                          client: MCP Streamable HTTP, stateless
//
// With WebUI, also the page at / and /static/, and sign-in at /auth/*; /api/*
// then accepts a signed-in browser's session cookie too.
func NewHandler(svc *Service, opts Options, log *slog.Logger) (http.Handler, error) {
	if !validHash(opts.BoardTokenHash) {
		return nil, errors.New("the board token hash must be 64 hex characters (see ptouch-command-server token)")
	}
	board := Tokens{opts.BoardTokenHash: "board"}
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

func (h handlers) next(w http.ResponseWriter, r *http.Request) {
	tape, err := strconv.Atoi(r.URL.Query().Get("tape_mm"))
	if err != nil || tape < 0 {
		writeError(w, refuse(http.StatusBadRequest, "bad_request", "tape_mm must be a number of mm, or 0"))
		return
	}
	problem := r.URL.Query().Get(wire.ProblemParam)
	if problem != "" && !problemCode.MatchString(problem) {
		writeError(w, refuse(http.StatusBadRequest, "bad_request", "problem must be a lower-case code like editor_lite"))
		return
	}
	job, err := h.svc.Next(r.Context(), tape, problem)
	switch {
	case err != nil:
		writeError(w, err)
	case job == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusOK, job)
	}
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
	if err := h.svc.Report(r.Context(), id, res); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// submit takes {"kind": "barcode", "text": "..."},
// {"kind": "text", "lines": ["...", "..."], "size": "large", "align": "left"} or
// {"kind": "image", "tape_mm": 24, "image": "<base64 PNG>"}.
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
	writeJSON(w, http.StatusOK, h.svc.Status())
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
