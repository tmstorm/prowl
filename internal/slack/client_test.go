package slack_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/slack"
)

func TestAuthTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth.test" || r.Header.Get("Authorization") != "Bearer xoxp-1" {
			fmt.Fprint(w, `{"ok":false,"error":"invalid_auth"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"team":"Acme","team_id":"T1","user":"sam","user_id":"U1"}`)
	}))
	defer srv.Close()

	id, err := slack.NewWith("xoxp-1", srv.URL+"/").AuthTest(context.Background())
	if err != nil || id.User != "sam" || id.TeamID != "T1" {
		t.Fatalf("AuthTest = %+v, %v", id, err)
	}
	_, err = slack.NewWith("xoxp-bad", srv.URL+"/").AuthTest(context.Background())
	var se *slack.Error
	if !errors.As(err, &se) || se.Code != "invalid_auth" {
		t.Fatalf("bad token: err = %v, want invalid_auth", err)
	}
}

func TestLogin(t *testing.T) {
	var challenge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		switch {
		case r.URL.Path != "/oauth.v2.access", r.Form.Get("code") != "the-code", r.Form.Get("client_id") != "123.456":
			fmt.Fprint(w, `{"ok":false,"error":"invalid_code"}`)
		case r.Form.Get("client_secret") != "", base64.RawURLEncoding.EncodeToString(sum[:]) != challenge:
			fmt.Fprint(w, `{"ok":false,"error":"invalid_code_verifier"}`)
		default:
			fmt.Fprint(w, `{"ok":true,"authed_user":{"id":"U1","access_token":"xoxp-new"}}`)
		}
	}))
	defer srv.Close()

	browser := func(authorize string) error {
		u, err := url.Parse(authorize)
		if err != nil {
			return err
		}
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != slack.RedirectURL || q.Get("user_scope") == "" {
			return fmt.Errorf("authorize URL %s", authorize)
		}
		challenge = q.Get("code_challenge")
		go func() {
			resp, err := http.Get(slack.RedirectURL + "?code=the-code&state=" + url.QueryEscape(q.Get("state")))
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	token, err := slack.NewWith("", srv.URL+"/").Login(ctx, "123.456", browser)
	if err != nil || token != "xoxp-new" {
		t.Fatalf("Login = %q, %v", token, err)
	}
}

// Shares reads every PR link in the matches, from channels, threads and DMs,
// once per message, and skips messages from before since.
func TestShares(t *testing.T) {
	since := time.Unix(1794800000, 0)
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query = r.Form.Get("query")
		fmt.Fprint(w, `{"ok":true,"messages":{"paging":{"pages":1},"matches":[
{"ts":"1794869000.000100","user":"U1","username":"alice","text":"<https://github.com/o/web/pull/41|github.com/o/web/pull/41> and https://github.com/o/api/pull/12 and <https://github.com/o/web/pull/41>",
 "permalink":"https://x.slack.com/archives/C1/p1794869000000100","channel":{"id":"C1","name":"reviews"}},
{"ts":"1794862000.000200","user":"U2","username":"bob","text":"<https://github.com/o/docs/pull/42>",
 "permalink":"https://x.slack.com/archives/C1/p1794862000000200?thread_ts=1794860000.000100&cid=C1","channel":{"id":"C1","name":"reviews"}},
{"ts":"1794867000.000300","user":"U3","username":"carol","text":"<https://github.com/o/docs/pull/43>",
 "permalink":"https://x.slack.com/archives/D9/p1794867000000300","channel":{"id":"D9","name":"U3","is_im":true}},
{"ts":"1794700000.000000","user":"U3","username":"carol","text":"https://github.com/o/old/pull/1",
 "permalink":"https://x.slack.com/archives/C1/p1794700000000000","channel":{"id":"C1","name":"reviews"}}
]}}`)
	}))
	defer srv.Close()

	shares, err := slack.NewWith("xoxp-1", srv.URL+"/").Shares(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if query != "github.com pull after:2026-11-14" {
		t.Errorf("query = %q", query)
	}
	var got []string
	for _, s := range shares {
		got = append(got, fmt.Sprintf("%s %s %s %v %s %s", s.Key(), s.Channel, s.Name, s.DM, s.ThreadTS, s.UserName))
	}
	want := []string{
		"o/web#41 C1 reviews false  alice",
		"o/api#12 C1 reviews false  alice",
		"o/docs#42 C1 reviews false 1794860000.000100 bob",
		"o/docs#43 D9  true  carol",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("shares:\n got %q\nwant %q", got, want)
	}
}
