package gh

import (
	"net/http"
	"sync"

	"github.com/cli/go-gh/v2/pkg/auth"
)

// tokenTransport picks up a new GitHub login without a restart: when a
// request comes back 401, it reads the token again (from the environment,
// gh's config or the keyring, as go-gh does) and retries once with it. Later
// requests use the new token too.
type tokenTransport struct {
	next http.RoundTripper
	host string

	mu    sync.Mutex
	token string // set once a refresh found a newer token
}

func (t *tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	token := t.token
	t.mu.Unlock()
	if token != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "token "+token)
	}
	resp, err := t.next.RoundTrip(r)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || r.Body != nil && r.GetBody == nil {
		return resp, err
	}
	fresh, _ := auth.TokenForHost(t.host)
	if fresh == "" || "token "+fresh == r.Header.Get("Authorization") {
		return resp, nil
	}
	retry := r.Clone(r.Context())
	if r.GetBody != nil {
		body, err := r.GetBody()
		if err != nil {
			return resp, nil
		}
		retry.Body = body
	}
	retry.Header.Set("Authorization", "token "+fresh)
	_ = resp.Body.Close()
	t.mu.Lock()
	t.token = fresh
	t.mu.Unlock()
	return t.next.RoundTrip(retry)
}
