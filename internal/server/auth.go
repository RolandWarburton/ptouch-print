package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/RolandWarburton/ptouch-print/internal/dexauth"
)

// Who may call what:
//   - /board/*: the board, with the one board token.
//   - /api/* and /mcp: clients, with a named client token or (when Dex is
//     configured) a Dex token for an allowed email, as bookstack-mcp does.
//
// The server keeps only SHA-256 hashes of the static tokens.

// NewToken makes a token and the hash the server is configured with.
func NewToken() (token, hash string) {
	token = rand.Text()
	return token, HashToken(token)
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Tokens maps token hashes to names.
type Tokens map[string]string

// ParseTokens reads "name:sha256,name:sha256".
func ParseTokens(list string) (Tokens, error) {
	t := Tokens{}
	names := map[string]bool{}
	for entry := range strings.SplitSeq(list, ",") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		name, hash, ok := strings.Cut(entry, ":")
		if !ok || name == "" || !validHash(hash) {
			return nil, fmt.Errorf("%q isn't name:sha256-hex", entry)
		}
		if names[name] {
			return nil, fmt.Errorf("token name %q is used twice", name)
		}
		names[name] = true
		t[strings.ToLower(hash)] = name
	}
	return t, nil
}

func validHash(h string) bool {
	b, err := hex.DecodeString(h)
	return err == nil && len(b) == sha256.Size
}

type identityKey struct{}

// Submitter is who the request was authenticated as: an email from Dex, or
// "token:<name>" for a static token.
func Submitter(ctx context.Context) string {
	who, _ := ctx.Value(identityKey{}).(string)
	return who
}

// authenticator checks a bearer token, returning who it belongs to, or an
// HTTP status and reason.
type authenticator interface {
	check(ctx context.Context, token string) (who string, status int, reason string)
}

func (t Tokens) check(_ context.Context, token string) (string, int, string) {
	if name, ok := t[HashToken(token)]; ok {
		return "token:" + name, 0, ""
	}
	return "", http.StatusUnauthorized, "missing or wrong bearer token"
}

// clientAuth accepts a named client token, or else a Dex token if Dex is set up.
type clientAuth struct {
	tokens Tokens
	dex    *dexAuth // nil without Dex
}

func (a clientAuth) check(ctx context.Context, token string) (string, int, string) {
	if who, status, reason := a.tokens.check(ctx, token); status == 0 || a.dex == nil {
		return who, status, reason
	}
	return a.dex.check(ctx, token)
}

var bearer = regexp.MustCompile(`(?i)^Bearer\s+(\S+)$`)

// requireAuth lets a request through only with a bearer token a accepts,
// putting the caller's identity in its context. challenge, if set, is the
// WWW-Authenticate header for a 401.
func requireAuth(a authenticator, challenge func(reason string) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, reason := http.StatusUnauthorized, "missing bearer token"
		if m := bearer.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
			var who string
			if who, status, reason = a.check(r.Context(), m[1]); status == 0 {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, who)))
				return
			}
		}
		if status == http.StatusUnauthorized && challenge != nil {
			w.Header().Set("WWW-Authenticate", challenge(reason))
		}
		writeError(w, &Error{Status: status, Code: "unauthorized", Msg: reason})
	})
}

// DexConfig mirrors bookstack-mcp's MCP_* settings.
type DexConfig struct {
	ResourceURL string // public URL of /mcp
	Issuer      string // Dex's URL, e.g. https://auth.example.com
	Audience    string // the Dex client; default ptouch-mcp
	Allowed     string // comma-separated emails
}

// dexAuth: claude.ai signs in through Dex and sends the access token; Dex's
// userinfo endpoint vouches for it, the token must have been issued to this
// server's Dex client, and the email must be allowed. Copied from bookstack-mcp.
type dexAuth struct {
	resourceURL, issuer, audience string
	allowed                       []string
	client                        *http.Client

	mu    sync.Mutex
	cache map[string]cached // sha256(token) -> caller
}

type cached struct {
	email string
	until time.Time
}

// userinfo answers are reused briefly so a burst of tool calls costs one Dex round trip.
const authCacheTTL = time.Minute

func newDexAuth(c DexConfig) (*dexAuth, error) {
	if c.ResourceURL == "" {
		return nil, errors.New("MCP_RESOURCE_URL must be set (the public URL of /mcp)")
	}
	if u, err := url.Parse(c.ResourceURL); err != nil || u.Host == "" {
		return nil, fmt.Errorf("MCP_RESOURCE_URL %q isn't a URL", c.ResourceURL)
	}
	if c.Issuer == "" {
		return nil, errors.New("MCP_OIDC_ISSUER must be set (Dex's URL)")
	}
	if c.Audience == "" {
		c.Audience = "ptouch-mcp"
	}
	emails := dexauth.ParseEmails(c.Allowed)
	if len(emails) == 0 {
		return nil, errors.New("MCP_ALLOWED_EMAILS must list who may print")
	}
	return &dexAuth{
		resourceURL: c.ResourceURL, issuer: strings.TrimRight(c.Issuer, "/"), audience: c.Audience, allowed: emails,
		client: &http.Client{Timeout: 10 * time.Second}, cache: map[string]cached{},
	}, nil
}

func (a *dexAuth) check(ctx context.Context, token string) (string, int, string) {
	key := HashToken(token)
	a.mu.Lock()
	hit, ok := a.cache[key]
	a.mu.Unlock()
	if ok && time.Now().Before(hit.until) {
		return hit.email, 0, ""
	}

	email, err := dexauth.Userinfo(ctx, a.client, a.issuer+"/userinfo", token)
	switch {
	case errors.Is(err, dexauth.ErrNoEmail):
		return "", http.StatusForbidden, "this account has no verified email"
	case errors.Is(err, dexauth.ErrRejected):
		return "", http.StatusUnauthorized, err.Error()
	case err != nil:
		return "", http.StatusServiceUnavailable, "can't reach Dex to check the token"
	}
	// Only tokens issued to this server's Dex client, so one meant for another app can't be replayed here.
	if !slices.Contains(dexauth.Audiences(token), a.audience) {
		return "", http.StatusUnauthorized, "token was not issued to " + a.audience
	}
	if !slices.Contains(a.allowed, email) {
		return "", http.StatusForbidden, email + " is not allowed"
	}

	now := time.Now()
	a.mu.Lock()
	a.cache[key] = cached{email, now.Add(authCacheTTL)}
	for k, v := range a.cache {
		if now.After(v.until) {
			delete(a.cache, k)
		}
	}
	a.mu.Unlock()
	return email, 0, ""
}

func (a *dexAuth) metadata() any {
	return map[string]any{
		"resource":                 a.resourceURL,
		"authorization_servers":    []string{a.issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"openid", "email", "profile", "offline_access"},
	}
}

func (a *dexAuth) challenge(reason string) string {
	u, _ := url.Parse(a.resourceURL)
	meta := u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource"
	return fmt.Sprintf(`Bearer resource_metadata="%s", error="invalid_token", error_description="%s"`,
		meta, strings.ReplaceAll(reason, `"`, "'"))
}
