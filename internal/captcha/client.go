// Package captcha verifies Cloudflare Turnstile response tokens, guarding
// Register against scripted account creation.
package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const verifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Verifier checks a Turnstile response token. An interface (same shape as
// auth.AppleAuthenticator) so auth_service_test.go can inject a fake instead
// of hitting the real Cloudflare endpoint.
type Verifier interface {
	Verify(ctx context.Context, token, remoteIP string) (bool, error)
}

// Client is the real Verifier, calling Cloudflare's siteverify endpoint.
type Client struct {
	secretKey  string
	verifyURL  string // swapped for a local httptest server in tests
	httpClient *http.Client
}

// New builds a Client. secretKey empty is allowed here — the misconfiguration
// is caught at Verify time, not construction time, since a nil-vs-configured
// Client isn't a meaningful distinction the caller needs to branch on.
func New(secretKey string) *Client {
	return &Client{
		secretKey:  secretKey,
		verifyURL:  verifyURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

type verifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

// Verify posts token to Cloudflare's siteverify endpoint. remoteIP is
// optional per Cloudflare's own contract — passing it strengthens the check
// with the caller's real address, but an empty string is a valid call.
//
// Deliberately not threading the caller's real IP through from the handler
// for this first pass — doing so needs new plumbing (nothing today carries a
// resolved client IP from internal/middleware's rate limiter into the
// service layer; it's computed and discarded inline) for a field Cloudflare
// documents as optional. Revisit if Cloudflare's risk scoring without it
// proves too permissive.
//
// An empty token returns (false, nil) — a normal "did not pass", not an
// error; a network/decode failure is logged here and returned as an error,
// letting the caller decide whether "couldn't verify" and "verification
// failed" should be treated the same (see auth_service.go — they are).
func (c *Client) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	if c.secretKey == "" {
		return false, errors.New("captcha: TURNSTILE_SECRET_KEY not configured")
	}
	if token == "" {
		return false, nil
	}

	form := url.Values{
		"secret":   {c.secretKey},
		"response": {token},
	}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.verifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("captcha: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("captcha: siteverify request failed: %v", err)
		return false, fmt.Errorf("captcha: siteverify request: %w", err)
	}
	defer resp.Body.Close()

	var result verifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("captcha: siteverify response decode failed (status %d): %v", resp.StatusCode, err)
		return false, fmt.Errorf("captcha: decode response: %w", err)
	}

	if !result.Success {
		log.Printf("captcha: verification failed, error-codes=%v", result.ErrorCodes)
	}

	return result.Success, nil
}
