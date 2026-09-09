package captcha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient points a Client at a local httptest server instead of
// Cloudflare's real siteverify endpoint, mirroring internal/plaid's
// newTestClient pattern.
func newTestClient(server *httptest.Server, secretKey string) *Client {
	return &Client{secretKey: secretKey, verifyURL: server.URL, httpClient: server.Client()}
}

func TestVerify_Success(t *testing.T) {
	var gotSecret, gotResponse, gotRemoteIP string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		gotSecret = r.FormValue("secret")
		gotResponse = r.FormValue("response")
		gotRemoteIP = r.FormValue("remoteip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success": true}`))
	}))
	defer server.Close()

	ok, err := newTestClient(server, "my-secret").Verify(context.Background(), "tok-123", "1.2.3.4")

	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "my-secret", gotSecret)
	assert.Equal(t, "tok-123", gotResponse)
	assert.Equal(t, "1.2.3.4", gotRemoteIP)
}

func TestVerify_CloudflareRejectsToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success": false, "error-codes": ["invalid-input-response"]}`))
	}))
	defer server.Close()

	ok, err := newTestClient(server, "my-secret").Verify(context.Background(), "bad-token", "")

	require.NoError(t, err, "a rejected token is a normal false, not an error")
	assert.False(t, ok)
}

func TestVerify_EmptyTokenSkipsTheNetworkCall(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success": true}`))
	}))
	defer server.Close()

	ok, err := newTestClient(server, "my-secret").Verify(context.Background(), "", "")

	require.NoError(t, err)
	assert.False(t, ok)
	assert.False(t, called, "an empty token should never reach Cloudflare")
}

func TestVerify_MissingSecretKeyIsAConfigError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not have called Cloudflare with no secret configured")
	}))
	defer server.Close()

	ok, err := newTestClient(server, "").Verify(context.Background(), "tok-123", "")

	require.Error(t, err)
	assert.False(t, ok)
}

func TestVerify_NetworkFailureIsReturnedAsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // closed before use — every request fails to connect

	ok, err := newTestClient(server, "my-secret").Verify(context.Background(), "tok-123", "")

	require.Error(t, err)
	assert.False(t, ok)
}

func TestVerify_MalformedResponseIsReturnedAsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()

	ok, err := newTestClient(server, "my-secret").Verify(context.Background(), "tok-123", "")

	require.Error(t, err)
	assert.False(t, ok)
}
