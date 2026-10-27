package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const webSecret = "web-secret"

// fakeWebDex is Dex for the browser sign-in: /token swaps a code for an access
// token after checking the client and the PKCE verifier, /userinfo names it.
func fakeWebDex(t *testing.T, codes map[string]string) *httptest.Server {
	challenges := map[string]string{} // code -> code_challenge, filled by the test
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			id, secret, _ := r.BasicAuth()
			r.ParseForm()
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			code := r.PostForm.Get("code")
			if id != webClientID || secret != webSecret || codes[code] == "" ||
				base64.RawURLEncoding.EncodeToString(sum[:]) != challenges[code] {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "at-" + code})
		case "/userinfo":
			code := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer at-")
			json.NewEncoder(w).Encode(map[string]string{"email": codes[code]})
		case "/challenge": // the test hands over the challenge the server sent the browser with
			challenges[r.URL.Query().Get("code")] = r.URL.Query().Get("challenge")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newWebEnv(t *testing.T, dex *httptest.Server) *env {
	return newEnvWith(t, Options{
		Dex:   &DexConfig{ResourceURL: "https://ptouch.example.com/mcp", Issuer: dex.URL, Allowed: "me@example.com"},
		WebUI: true, WebSecret: webSecret,
	})
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// send makes a request with the given cookies; httptest is plain HTTP, so the
// Secure cookies are carried by hand.
func send(t *testing.T, method, url, contentType, body string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	res, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res
}

func cookie(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// signIn goes through /auth/login and the callback as Dex would send the
// browser back with code, returning the callback's answer.
func signIn(t *testing.T, e *env, dex *httptest.Server, code string) *http.Response {
	t.Helper()
	res := send(t, "GET", e.url+"/auth/login", "", "")
	loc, err := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusFound || err != nil || !strings.HasPrefix(loc.String(), dex.URL+"/auth?") {
		t.Fatalf("login: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	q := loc.Query()
	if q.Get("client_id") != webClientID || q.Get("redirect_uri") != "https://ptouch.example.com/auth/callback" {
		t.Fatalf("login query %v", q)
	}
	send(t, "GET", dex.URL+"/challenge?"+url.Values{"code": {code}, "challenge": {q.Get("code_challenge")}}.Encode(), "", "")
	login := cookie(res, loginCookie)
	if login == nil || login.SameSite != http.SameSiteLaxMode || !login.Secure || !login.HttpOnly {
		t.Fatalf("login cookie %+v", login)
	}
	return send(t, "GET", e.url+"/auth/callback?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), "", "", login)
}

func TestWebSignIn(t *testing.T) {
	dex := fakeWebDex(t, map[string]string{"mine": "Me@Example.com", "eve": "eve@example.com"})
	e := newWebEnv(t, dex)
	e.poll("24")

	if res := send(t, "GET", e.url+"/", "", ""); res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if res := send(t, "GET", e.url+"/static/app.js", "", ""); res.StatusCode != 200 {
		t.Fatalf("app.js: %d", res.StatusCode)
	}
	if res := send(t, "GET", e.url+"/auth/me", "", ""); res.StatusCode != 401 {
		t.Fatalf("me before sign-in: %d", res.StatusCode)
	}

	res := signIn(t, e, dex, "mine")
	session := cookie(res, sessionCookie)
	if res.StatusCode != http.StatusSeeOther || session == nil || session.SameSite != http.SameSiteStrictMode ||
		!session.Secure || !session.HttpOnly || session.Path != "/" {
		t.Fatalf("callback: %d %+v", res.StatusCode, session)
	}
	if res := send(t, "GET", e.url+"/auth/me", "", "", session); res.StatusCode != 200 {
		t.Fatalf("me: %d", res.StatusCode)
	}
	if res := send(t, "GET", e.url+"/api/status", "", "", session); res.StatusCode != 200 {
		t.Errorf("status with session: %d", res.StatusCode)
	}
	if res := send(t, "POST", e.url+"/api/jobs", "application/json", `{"kind":"barcode","text":"A"}`, session); res.StatusCode != 201 {
		t.Fatalf("submit with session: %d", res.StatusCode)
	}
	if j, _ := e.svc.Get(t.Context(), 1); j.Submitter != "me@example.com" {
		t.Errorf("submitter %q", j.Submitter)
	}
	// A form post, as another site could make, isn't JSON.
	if res := send(t, "POST", e.url+"/api/jobs", "application/x-www-form-urlencoded", "kind=barcode&text=A", session); res.StatusCode != 415 {
		t.Errorf("form post with session: %d", res.StatusCode)
	}
	// The session is for /api only, not /mcp or the board.
	if res := send(t, "POST", e.url+"/mcp", "application/json", "{}", session); res.StatusCode != 401 {
		t.Errorf("mcp with session: %d", res.StatusCode)
	}
	if res := send(t, "GET", e.url+"/board/next?tape_mm=24", "", "", session); res.StatusCode != 401 {
		t.Errorf("board with session: %d", res.StatusCode)
	}
	tampered := *session
	tampered.Value = base64.RawURLEncoding.EncodeToString([]byte("eve@example.com")) + session.Value[strings.IndexByte(session.Value, '.'):]
	if res := send(t, "GET", e.url+"/api/status", "", "", &tampered); res.StatusCode != 401 {
		t.Errorf("tampered session: %d", res.StatusCode)
	}

	res = send(t, "POST", e.url+"/auth/logout", "", "", session)
	if c := cookie(res, sessionCookie); res.StatusCode != 204 || c == nil || c.MaxAge >= 0 {
		t.Errorf("logout: %d %+v", res.StatusCode, c)
	}

	if res := signIn(t, e, dex, "eve"); res.StatusCode != 403 || cookie(res, sessionCookie) != nil {
		t.Errorf("email not allowed: %d", res.StatusCode)
	}
}

func TestWebCallbackNeedsState(t *testing.T) {
	dex := fakeWebDex(t, map[string]string{"mine": "me@example.com"})
	e := newWebEnv(t, dex)
	login := cookie(send(t, "GET", e.url+"/auth/login", "", ""), loginCookie)
	if res := send(t, "GET", e.url+"/auth/callback?code=mine&state=forged", "", "", login); res.StatusCode != 400 {
		t.Errorf("wrong state: %d", res.StatusCode)
	}
	if res := send(t, "GET", e.url+"/auth/callback?code=mine&state=x", "", ""); res.StatusCode != 400 {
		t.Errorf("no login cookie: %d", res.StatusCode)
	}
}

func TestWebSession(t *testing.T) {
	web := &webUI{dex: &dexAuth{allowed: []string{"me@example.com"}}, key: []byte("k")}
	req := func(value string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
		return r
	}
	if email, ok := web.session(req(web.seal("me@example.com", time.Hour))); !ok || email != "me@example.com" {
		t.Errorf("good session: %q %v", email, ok)
	}
	if _, ok := web.session(req(web.seal("me@example.com", -time.Second))); ok {
		t.Error("expired session accepted")
	}
	// Taking someone off MCP_ALLOWED_EMAILS ends their session at once.
	if _, ok := web.session(req(web.seal("gone@example.com", time.Hour))); ok {
		t.Error("session for an email no longer allowed")
	}
	other := &webUI{dex: web.dex, key: []byte("another startup")}
	if _, ok := web.session(req(other.seal("me@example.com", time.Hour))); ok {
		t.Error("session signed with another key (a restart) accepted")
	}
}

func TestWebUIOff(t *testing.T) {
	e := newEnv(t, nil)
	for _, path := range []string{"/", "/static/app.js", "/auth/login", "/auth/me"} {
		if res := send(t, "GET", e.url+path, "", ""); res.StatusCode != 404 {
			t.Errorf("%s with WEB_UI off: %d", path, res.StatusCode)
		}
	}
}

func TestWebUINeedsDexAndSecret(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dex := &DexConfig{ResourceURL: "https://ptouch.example.com/mcp", Issuer: "https://auth.example.com", Allowed: "me@example.com"}
	for name, opts := range map[string]Options{
		"no Dex":    {WebUI: true, WebSecret: webSecret},
		"no secret": {WebUI: true, Dex: dex},
	} {
		opts.BoardTokenHash = HashToken(boardToken)
		if _, err := NewHandler(nil, opts, log); err == nil {
			t.Errorf("%s: started", name)
		}
	}
}
