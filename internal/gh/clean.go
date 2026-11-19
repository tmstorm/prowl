package gh

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// cleanTransport takes terminal control characters out of diffs: they are
// other people's text, and an escape sequence in them could set your
// clipboard or redraw your terminal when prowl shows it. go-gh already
// cleans JSON answers (titles, bodies, comments); job logs are cleaned line
// by line as they are read, and release downloads are binary.
//
// It also logs each request at debug level, and keeps the rate limit GitHub
// last reported.
type cleanTransport struct {
	next     http.RoundTripper
	rateLeft *atomic.Int64
}

func (t cleanTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.next.RoundTrip(r)
	if err != nil {
		slog.Debug("github", "method", r.Method, "path", r.URL.Path, "took", time.Since(start).Round(time.Millisecond), "err", err)
	} else {
		slog.Debug("github", "method", r.Method, "path", r.URL.Path, "status", resp.StatusCode, "took", time.Since(start).Round(time.Millisecond))
		if n, perr := strconv.ParseInt(resp.Header.Get("X-RateLimit-Remaining"), 10, 64); perr == nil {
			t.rateLeft.Store(n)
		}
	}
	if err != nil || resp.Body == nil || strings.Contains(resp.Header.Get("Content-Type"), "json") || strings.HasSuffix(r.URL.Path, "/logs") || r.Header.Get("Accept") == "application/octet-stream" {
		return resp, err
	}
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	b = []byte(Clean(string(b)))
	resp.Body = io.NopCloser(bytes.NewReader(b))
	resp.ContentLength = int64(len(b))
	resp.Header.Del("Content-Length")
	return resp, nil
}

// Clean replaces control characters other than tab and newline with
// U+FFFD, so text from GitHub or an agent can't drive the terminal. Windows
// line endings become plain newlines.
func Clean(s string) string {
	if !strings.ContainsFunc(s, control) {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		if control(r) {
			return utf8.RuneError
		}
		return r
	}, s)
}

func control(r rune) bool {
	return r < 0x20 && r != '\t' && r != '\n' || r == 0x7f || r >= 0x80 && r <= 0x9f
}
