package slack

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

// RedirectURL is where Slack sends the browser after sign-in. It must match
// the app's redirect URL exactly.
const RedirectURL = "http://localhost:53682/callback"

// UserScopes are what prowl asks for at sign-in.
var UserScopes = []string{
	"channels:history", "groups:history", "im:history",
	"channels:read", "groups:read", "im:read",
	"search:read",
	"chat:write", "im:write",
	"reactions:read", "reactions:write",
	"users:read", "users:read.email",
}

// Manifest creates a Slack app prowl can sign in to.
func Manifest() string {
	return "display_information:\n  name: prowl\n  description: PR reviews from your terminal\n" +
		"oauth_config:\n  pkce_enabled: true\n  redirect_urls:\n    - " + RedirectURL + "\n" +
		"  scopes:\n    user:\n      - " + strings.Join(UserScopes, "\n      - ") + "\n" +
		"settings:\n  org_deploy_enabled: false\n  socket_mode_enabled: false\n  token_rotation_enabled: false\n"
}

// Client calls the Slack Web API as one user.
type Client struct {
	token string
	api   string
	http  *http.Client
}

// New returns a client for the Slack Web API.
func New(token string) *Client {
	return NewWith(token, "https://slack.com/api/")
}

// NewWith returns a client for the Web API at api, e.g. an httptest server.
func NewWith(token, api string) *Client {
	return &Client{token: token, api: api, http: &http.Client{Timeout: 30 * time.Second}}
}

// Error is an error Slack answered with, e.g. "invalid_auth".
type Error struct {
	Method string
	Code   string
}

func (e *Error) Error() string { return "slack " + e.Method + ": " + e.Code }

func (c *Client) call(ctx context.Context, method string, params url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api+method, strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("slack %s: rate limited, retry after %ss", method, resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack %s: %s", method, resp.Status)
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	var status struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	if !status.OK {
		return &Error{Method: method, Code: status.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Identity is who a token signs in as.
type Identity struct {
	Team   string `json:"team"`
	TeamID string `json:"team_id"`
	User   string `json:"user"`
	UserID string `json:"user_id"`
}

// AuthTest checks the token and says whose it is.
func (c *Client) AuthTest(ctx context.Context) (Identity, error) {
	var id Identity
	err := c.call(ctx, "auth.test", nil, &id)
	return id, err
}

// Login signs in through the browser with PKCE and returns the user token.
// open shows the authorize URL; Slack then redirects to RedirectURL, which
// Login listens on until ctx ends.
func (c *Client) Login(ctx context.Context, clientID string, open func(string) error) (string, error) {
	verifier, state := randomString(), randomString()
	sum := sha256.Sum256([]byte(verifier))
	redirect, _ := url.Parse(RedirectURL)
	ln, err := net.Listen("tcp", redirect.Host)
	if err != nil {
		return "", fmt.Errorf("listen for the Slack sign-in on %s: %w", redirect.Host, err)
	}
	codes := make(chan string, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != redirect.Path || q.Get("state") != state {
			http.NotFound(w, r)
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, "Slack sign-in failed: "+e, http.StatusBadRequest)
			select {
			case codes <- "":
			default:
			}
			return
		}
		fmt.Fprintln(w, "Signed in to Slack. You can close this tab and go back to prowl.")
		select {
		case codes <- q.Get("code"):
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	auth := url.Values{
		"client_id":             {clientID},
		"user_scope":            {strings.Join(UserScopes, ",")},
		"redirect_uri":          {RedirectURL},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	if err := open("https://slack.com/oauth/v2/authorize?" + auth.Encode()); err != nil {
		return "", err
	}
	var code string
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("slack sign-in: %w", ctx.Err())
	case code = <-codes:
	}
	if code == "" {
		return "", errors.New("slack sign-in was cancelled")
	}
	var out struct {
		AuthedUser struct {
			AccessToken string `json:"access_token"`
		} `json:"authed_user"`
	}
	err = c.call(ctx, "oauth.v2.access", url.Values{
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {RedirectURL},
	}, &out)
	if err != nil {
		return "", err
	}
	if out.AuthedUser.AccessToken == "" {
		return "", errors.New("slack sign-in returned no user token")
	}
	return out.AuthedUser.AccessToken, nil
}

// React adds your emoji to a message. One already there is fine.
func (c *Client) React(ctx context.Context, channel, ts, emoji string) error {
	err := c.call(ctx, "reactions.add", url.Values{"channel": {channel}, "timestamp": {ts}, "name": {emoji}}, nil)
	if se, ok := errors.AsType[*Error](err); ok && se.Code == "already_reacted" {
		return nil
	}
	return err
}

// Unreact removes your emoji from a message. One already gone is fine.
func (c *Client) Unreact(ctx context.Context, channel, ts, emoji string) error {
	err := c.call(ctx, "reactions.remove", url.Values{"channel": {channel}, "timestamp": {ts}, "name": {emoji}}, nil)
	if se, ok := errors.AsType[*Error](err); ok && se.Code == "no_reaction" {
		return nil
	}
	return err
}

// Post sends text to a channel as you, in the thread threadTS when set.
func (c *Client) Post(ctx context.Context, channel, threadTS, text string) error {
	v := url.Values{"channel": {channel}, "text": {text}, "unfurl_links": {"false"}}
	if threadTS != "" {
		v.Set("thread_ts", threadTS)
	}
	return c.call(ctx, "chat.postMessage", v, nil)
}

// User is a person in the Slack workspace.
type User struct {
	ID       string
	Handle   string // e.g. "alice"
	RealName string
	Display  string // the display name, often empty
	Email    string
}

// Users lists the workspace's people, without bots or deactivated accounts.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var out []User
	cursor := ""
	for range 20 {
		var res struct {
			Members []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Deleted  bool   `json:"deleted"`
				IsBot    bool   `json:"is_bot"`
				RealName string `json:"real_name"`
				Profile  struct {
					DisplayName string `json:"display_name"`
					Email       string `json:"email"`
				} `json:"profile"`
			} `json:"members"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := c.call(ctx, "users.list", url.Values{"limit": {"200"}, "cursor": {cursor}}, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Members {
			if !m.Deleted && !m.IsBot && m.ID != "USLACKBOT" {
				out = append(out, User{ID: m.ID, Handle: m.Name, RealName: m.RealName, Display: m.Profile.DisplayName, Email: m.Profile.Email})
			}
		}
		if cursor = res.Meta.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// DM sends text to a person in a direct message, as you.
func (c *Client) DM(ctx context.Context, user, text string) error {
	var res struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := c.call(ctx, "conversations.open", url.Values{"users": {user}}, &res); err != nil {
		return err
	}
	return c.Post(ctx, res.Channel.ID, "", text)
}

var userID = regexp.MustCompile(`^[UW][A-Z0-9]{6,}$`)

// IsUserID reports whether a slack_names value is a Slack user ID rather
// than a name to print as it is.
func IsUserID(s string) bool { return userID.MatchString(s) }

// Share is one PR link in a Slack message.
type Share struct {
	Repo     string // owner/name
	Number   int
	Channel  string // ID
	Name     string // channel name; empty for a DM
	DM       bool   // a DM or group DM
	TS       string // the message
	ThreadTS string // the thread it's in; empty at the top level
	User     string // who posted it (ID)
	UserName string
	At       time.Time
}

// Key is the PR as prowl names it, e.g. owner/name#12.
func (s Share) Key() string { return fmt.Sprintf("%s#%d", s.Repo, s.Number) }

var prLink = regexp.MustCompile(`https://github\.com/([\w.-]+/[\w.-]+)/pull/(\d+)`)

// maxSearchPages caps one sync's search: 100 matches a page.
const maxSearchPages = 5

// Shares finds the PR links posted since since, newest first, in every
// channel and DM you can read. Search works by day, so it reaches a little
// further back.
func (c *Client) Shares(ctx context.Context, since time.Time) ([]Share, error) {
	var out []Share
	for page := 1; page <= maxSearchPages; page++ {
		var res struct {
			Messages struct {
				Matches []struct {
					TS        string `json:"ts"`
					Text      string `json:"text"`
					User      string `json:"user"`
					Username  string `json:"username"`
					Permalink string `json:"permalink"`
					Channel   struct {
						ID     string `json:"id"`
						Name   string `json:"name"`
						IsIM   bool   `json:"is_im"`
						IsMPIM bool   `json:"is_mpim"`
					} `json:"channel"`
				} `json:"matches"`
				Paging struct {
					Pages int `json:"pages"`
				} `json:"paging"`
			} `json:"messages"`
		}
		err := c.call(ctx, "search.messages", url.Values{
			"query": {"github.com pull after:" + since.UTC().AddDate(0, 0, -2).Format(time.DateOnly)},
			"count": {"100"}, "sort": {"timestamp"}, "sort_dir": {"desc"}, "page": {strconv.Itoa(page)},
		}, &res)
		if err != nil {
			return nil, err
		}
		for _, m := range res.Messages.Matches {
			at, err := strconv.ParseFloat(m.TS, 64)
			if err != nil || time.Unix(int64(at), 0).Before(since) {
				continue
			}
			s := Share{Channel: m.Channel.ID, DM: m.Channel.IsIM || m.Channel.IsMPIM, TS: m.TS,
				User: m.User, UserName: m.Username, At: time.Unix(int64(at), 0)}
			if !s.DM {
				s.Name = m.Channel.Name
			}
			if u, err := url.Parse(m.Permalink); err == nil {
				s.ThreadTS = u.Query().Get("thread_ts")
			}
			seen := map[string]bool{}
			for _, l := range prLink.FindAllStringSubmatch(m.Text, -1) {
				s.Repo = l[1]
				s.Number, _ = strconv.Atoi(l[2])
				if !seen[s.Key()] {
					seen[s.Key()] = true
					out = append(out, s)
				}
			}
		}
		if page >= res.Messages.Paging.Pages {
			break
		}
	}
	return out, nil
}

func randomString() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

const keyringService, keyringUser = "prowl", "slack"

// Token is the saved user token; PROWL_SLACK_TOKEN overrides the keyring,
// for machines without one. Empty means not connected.
func Token() (string, error) {
	if t := os.Getenv("PROWL_SLACK_TOKEN"); t != "" {
		return t, nil
	}
	t, err := keyring.Get(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the Slack token from the keyring: %w", err)
	}
	return t, nil
}

// SaveToken keeps the user token in the OS keyring.
func SaveToken(token string) error {
	if err := keyring.Set(keyringService, keyringUser, token); err != nil {
		return fmt.Errorf("save the Slack token in the keyring: %w", err)
	}
	return nil
}

// DeleteToken removes the user token from the OS keyring.
func DeleteToken() error {
	err := keyring.Delete(keyringService, keyringUser)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete the Slack token from the keyring: %w", err)
	}
	return nil
}
