package gh

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// cleanTransport takes terminal control characters out of diffs: they are
// other people's text, and an escape sequence in them could set your
// clipboard or redraw your terminal when prowl shows it. go-gh already
// cleans JSON answers (titles, bodies, comments); job logs are cleaned line
// by line as they are read, and release downloads are binary.
type cleanTransport struct{ next http.RoundTripper }

func (t cleanTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
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
