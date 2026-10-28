package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/RolandWarburton/ptouch-print/internal/dexauth"
)

// The web UI (WEB_UI): the static page under web/, and sign-in through Dex
// for people in a browser; / sends anyone not signed in straight to Dex. A signed-in browser holds a session cookie that
// /api/* accepts in place of a bearer token. The cookie names an allowed email
// and is signed with a key made at startup, so a restart signs everyone out.

//go:embed web
var webFiles embed.FS

const (
	webClientID   = "ptouch-web" // the Dex client the browser signs in with
	sessionCookie = "ptouch_session"
	loginCookie   = "ptouch_login" // state and PKCE verifier between /auth/login and the callback
	sessionLife   = 30 * 24 * time.Hour
	loginLife     = 10 * time.Minute
)

type webUI struct {
	dex      *dexAuth
	secret   string // ptouch-web's client secret
	redirect string // this server's /auth/callback
	key      []byte
	client   *http.Client
}

func newWebUI(dex *dexAuth, secret string) (*webUI, error) {
	if secret == "" {
		return nil, errors.New("PTOUCH_WEB_OIDC_SECRET must be set (the secret of Dex client " + webClientID + ")")
	}
	u, _ := url.Parse(dex.resourceURL)
	key := make([]byte, 32)
	rand.Read(key)
	return &webUI{
		dex: dex, secret: secret, redirect: u.Scheme + "://" + u.Host + "/auth/callback", key: key,
		client: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (u *webUI) routes(mux *http.ServeMux) {
	root, _ := fs.Sub(webFiles, "web")
	files := http.FileServerFS(root)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := u.session(r); !ok {
			http.Redirect(w, r, "/auth/login", http.StatusFound)
			return
		}
		files.ServeHTTP(w, r)
	})
	mux.Handle("GET /static/", files)
	mux.HandleFunc("GET /auth/login", u.login)
	mux.HandleFunc("GET /auth/callback", u.callback)
	mux.HandleFunc("POST /auth/logout", u.logout)
	mux.HandleFunc("GET /auth/me", u.me)
}

// orSession serves a request with no Authorization header but a valid session
// as the signed-in email; everything else goes to bearer. With SameSite=Lax on
// the cookie and JSON required on writes, another site can't submit for you.
func (u *webUI) orSession(bearer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email, ok := u.session(r)
		if !ok || r.Header.Get("Authorization") != "" {
			bearer.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				writeError(w, refuse(http.StatusUnsupportedMediaType, "bad_request", "body must be application/json"))
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, email)))
	})
}

// session is the email of a signed-in browser, if it is still allowed.
func (u *webUI) session(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	email, ok := u.open(c.Value)
	return email, ok && slices.Contains(u.dex.allowed, email)
}

func (u *webUI) me(w http.ResponseWriter, r *http.Request) {
	email, ok := u.session(r)
	if !ok {
		writeError(w, &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Msg: "not signed in"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": email})
}

func (u *webUI) login(w http.ResponseWriter, r *http.Request) {
	state, verifier := rand.Text(), rand.Text()+rand.Text() // PKCE wants 43 to 128 characters
	u.setCookie(w, loginCookie, "/auth/", u.seal(state+" "+verifier, loginLife), loginLife, http.SameSiteLaxMode)
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id":             {webClientID},
		"redirect_uri":          {u.redirect},
		"response_type":         {"code"},
		"scope":                 {"openid email"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, u.dex.issuer+"/auth?"+q.Encode(), http.StatusFound)
}

func (u *webUI) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		http.Error(w, "Dex refused the sign-in: "+e, http.StatusForbidden)
		return
	}
	c, err := r.Cookie(loginCookie)
	var saved string
	if err == nil {
		saved, _ = u.open(c.Value)
	}
	state, verifier, _ := strings.Cut(saved, " ")
	if state == "" || q.Get("state") != state {
		http.Error(w, "The sign-in expired or came from elsewhere. Go back and sign in again.", http.StatusBadRequest)
		return
	}
	u.setCookie(w, loginCookie, "/auth/", "", -1, http.SameSiteLaxMode)

	email, err := u.exchange(r.Context(), q.Get("code"), verifier)
	switch {
	case errors.Is(err, dexauth.ErrNoEmail):
		http.Error(w, "This account has no verified email.", http.StatusForbidden)
		return
	case err != nil:
		http.Error(w, "Couldn't finish signing in with Dex: "+err.Error(), http.StatusBadGateway)
		return
	case !slices.Contains(u.dex.allowed, email):
		http.Error(w, email+" is not allowed to print.", http.StatusForbidden)
		return
	}
	// Lax, not Strict: the redirect to / follows Dex's, so the browser counts it
	// as cross-site, and a Strict cookie would be left off and send you back to Dex.
	u.setCookie(w, sessionCookie, "/", u.seal(email, sessionLife), sessionLife, http.SameSiteLaxMode)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// exchange trades the authorization code for an access token, then asks Dex
// whose it is.
func (u *webUI) exchange(ctx context.Context, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {u.redirect},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.dex.issuer+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(webClientID), url.QueryEscape(u.secret))
	res, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&tok) != nil || tok.AccessToken == "" {
		return "", errors.New("the token endpoint answered " + res.Status)
	}
	return dexauth.Userinfo(ctx, u.client, u.dex.issuer+"/userinfo", tok.AccessToken)
}

func (u *webUI) logout(w http.ResponseWriter, _ *http.Request) {
	u.setCookie(w, sessionCookie, "/", "", -1, http.SameSiteLaxMode)
	w.WriteHeader(http.StatusNoContent)
}

// setCookie sets a cookie for life, or deletes it when life is negative.
func (u *webUI) setCookie(w http.ResponseWriter, name, path, value string, life time.Duration, site http.SameSite) {
	maxAge := int(life / time.Second)
	if life < 0 {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: site,
	})
}

// seal signs value with an expiry: base64(value).expiry.base64(hmac).
func (u *webUI) seal(value string, life time.Duration) string {
	body := base64.RawURLEncoding.EncodeToString([]byte(value)) + "." +
		strconv.FormatInt(time.Now().Add(life).Unix(), 10)
	return body + "." + base64.RawURLEncoding.EncodeToString(u.mac(body))
}

// open returns what seal signed, if the signature holds and it hasn't expired.
func (u *webUI) open(sealed string) (string, bool) {
	i := strings.LastIndexByte(sealed, '.')
	if i < 0 {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sealed[i+1:])
	if err != nil || !hmac.Equal(sig, u.mac(sealed[:i])) {
		return "", false
	}
	value, expiry, _ := strings.Cut(sealed[:i], ".")
	until, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil || time.Now().Unix() > until {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	return string(b), err == nil
}

func (u *webUI) mac(body string) []byte {
	m := hmac.New(sha256.New, u.key)
	m.Write([]byte(body))
	return m.Sum(nil)
}
