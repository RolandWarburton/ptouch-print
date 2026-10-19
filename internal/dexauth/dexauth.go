// Package dexauth holds the Dex (OpenID Connect) checks for tokens claude.ai
// sends to /mcp. It trusts Dex's userinfo endpoint for the caller's email.
// Copied from bookstack-mcp, which shares the same Dex; keep them alike.
package dexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var (
	// ErrRejected means Dex didn't accept the token.
	ErrRejected = errors.New("Dex rejected the token")
	// ErrNoEmail means Dex accepted the token but gave no verified email.
	ErrNoEmail = errors.New("no verified email")
)

// ParseEmails splits a comma-separated list into lower-case emails.
func ParseEmails(list string) []string {
	var out []string
	for _, e := range strings.Split(list, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// Userinfo asks Dex's userinfo endpoint whose token this is, returning the
// lower-case email. Any other error means Dex couldn't be reached.
func Userinfo(ctx context.Context, client *http.Client, endpoint, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w (%d)", ErrRejected, res.StatusCode)
	}
	var info struct {
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
	}
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("%w: unreadable userinfo response", ErrRejected)
	}
	if info.Email == "" || info.EmailVerified != nil && !*info.EmailVerified {
		return "", ErrNoEmail
	}
	return strings.ToLower(info.Email), nil
}

// Audiences reads a JWT's aud claim. Not a signature check: use it only on a
// token userinfo has already vouched for.
func Audiences(token string) []string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims struct {
		Aud json.RawMessage `json:"aud"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	var one string
	if json.Unmarshal(claims.Aud, &one) == nil {
		return []string{one}
	}
	var many []string
	json.Unmarshal(claims.Aud, &many)
	return many
}
