package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/slack"
)

// NameMatch proposes the Slack user for a GitHub login.
type NameMatch struct {
	Login string
	User  SlackUser // zero when nothing matched
	Why   string    // e.g. "same name"
}

// ProposeNames guesses the Slack user of everyone in your orgs, and every
// author in your inbox, that slack_names doesn't map to a Slack user yet. From surest to
// least: the same email, the same name, a login that holds their Slack name
// or handle, then whoever posts most of their PRs in Slack. A Slack user is
// proposed for one login at most.
func (co *Core) ProposeNames() ([]NameMatch, []SlackUser, error) {
	co.mu.Lock()
	sl, err := co.slackFor()
	cfg, viewer, me := co.cfg, co.inbox.Viewer, co.slackMe
	items := slices.Clone(co.inbox.Items)
	co.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	users, err := co.loadSlackUsers(sl)
	if err != nil {
		return nil, nil, err
	}
	var people []gh.Person
	for _, org := range cfg.Orgs {
		members, err := co.gh.OrgMembers(org)
		if err != nil {
			return nil, nil, err
		}
		people = append(people, members...)
	}
	botLogins := cfg.BotSet()
	for _, it := range items {
		a := it.PR.AuthorLogin()
		if a != "" && !it.PR.Author.IsBot(botLogins) && !slices.ContainsFunc(people, func(p gh.Person) bool { return p.Login == a }) {
			people = append(people, gh.Person{Login: a})
		}
	}
	people = slices.DeleteFunc(people, func(p gh.Person) bool {
		return slack.IsUserID(cfg.SlackNames[p.Login])
	})
	shares, err := co.st.SlackShares()
	if err != nil {
		return nil, nil, err
	}
	authors := map[string]string{}
	for _, it := range items {
		authors[it.PR.Key()] = it.PR.AuthorLogin()
	}
	posted := map[string]map[string]int{}
	for _, s := range shares {
		if a := authors[s.PRKey]; a != "" {
			if posted[a] == nil {
				posted[a] = map[string]int{}
			}
			posted[a][s.User]++
		}
	}
	out := make([]SlackUser, 0, len(users))
	for _, u := range users {
		out = append(out, slackUserOf(u))
	}
	return matchNames(people, users, posted, viewer, me), out, nil
}

// SlackUser is a person in your Slack workspace.
type SlackUser struct {
	ID       string
	Handle   string // e.g. "alice"
	RealName string
	Display  string // the display name, often empty
}

func slackUserOf(u slack.User) SlackUser {
	return SlackUser{ID: u.ID, Handle: u.Handle, RealName: u.RealName, Display: u.Display}
}

// HasSlackUser reports whether slack_names maps login to a Slack user.
func (co *Core) HasSlackUser(login string) bool {
	co.mu.Lock()
	defer co.mu.Unlock()
	return slack.IsUserID(co.cfg.SlackNames[login])
}

// SlackConnected reports whether you're signed in to Slack.
func (co *Core) SlackConnected() bool {
	co.mu.Lock()
	defer co.mu.Unlock()
	return co.slack != nil
}

// matchNames runs the signals in order, each over the logins still open.
// posted counts, per login, the Slack users who posted their PRs. You are
// viewer on GitHub and me on Slack.
func matchNames(people []gh.Person, users []slack.User, posted map[string]map[string]int, viewer, me string) []NameMatch {
	found := map[string]NameMatch{}
	taken := map[string]bool{}
	signals := []struct {
		why   string
		match func(p gh.Person, u slack.User) bool
	}{
		{"you, from your Slack sign-in", func(p gh.Person, u slack.User) bool { return p.Login == viewer && u.ID == me }},
		{"same email", func(p gh.Person, u slack.User) bool { return p.Email != "" && strings.EqualFold(p.Email, u.Email) }},
		{"same name", func(p gh.Person, u slack.User) bool {
			n := fold(p.Name)
			return n != "" && (n == fold(u.RealName) || n == fold(u.Display))
		}},
		{"login matches their Slack name", func(p gh.Person, u slack.User) bool {
			for _, part := range strings.FieldsFunc(p.Login, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
				if n := fold(part); len(n) >= 3 && (n == fold(u.Handle) || n == fold(u.RealName) || n == fold(u.Display)) {
					return true
				}
			}
			login := fold(p.Login)
			if h := fold(u.Handle); len(h) >= 4 && strings.HasPrefix(login, h) {
				return true
			}
			return nameIn(u.RealName, login)
		}},
		{"posts their PRs", func(p gh.Person, u slack.User) bool {
			best := 0
			for _, n := range posted[p.Login] {
				best = max(best, n)
			}
			n := posted[p.Login][u.ID]
			return n > 0 && n == best
		}},
	}
	for _, sig := range signals {
		for _, p := range people {
			if _, ok := found[p.Login]; ok {
				continue
			}
			var hits []slack.User
			for _, u := range users {
				if !taken[u.ID] && sig.match(p, u) {
					hits = append(hits, u)
				}
			}
			if len(hits) == 1 {
				found[p.Login] = NameMatch{Login: p.Login, User: slackUserOf(hits[0]), Why: sig.why}
				taken[hits[0].ID] = true
			}
		}
	}
	out := make([]NameMatch, 0, len(people))
	for _, p := range people {
		out = append(out, cmp.Or(found[p.Login], NameMatch{Login: p.Login}))
	}
	return out
}

// nameIn reports whether every word of a full name of two words or more is
// in login, in order: "Mira Vance" is in "mirajvance".
func nameIn(name, login string) bool {
	var words []string
	for _, w := range strings.Fields(name) {
		if w = fold(w); len(w) >= 2 {
			words = append(words, w)
		}
	}
	if len(words) < 2 {
		return false
	}
	for _, w := range words {
		i := strings.Index(login, w)
		if i < 0 {
			return false
		}
		login = login[i+len(w):]
	}
	return true
}

// fold makes names comparable: letters and digits only, lower case, without
// accents, so "Muñoz" matches "munoz".
func fold(s string) string {
	s, _, _ = transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn))), s)
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

// loadSlackUsers reads the workspace's people once per process.
func (co *Core) loadSlackUsers(sl *slack.Client) ([]slack.User, error) {
	co.mu.Lock()
	cached := co.slackUsers
	co.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	users, err := sl.Users(context.Background())
	if err != nil {
		return nil, fmt.Errorf("slack users: %w", err)
	}
	co.mu.Lock()
	co.slackUsers = users
	co.mu.Unlock()
	return users, nil
}

// reviewStates are the states GitHub gives reviews posted with each event.
var reviewStates = map[ReviewEvent]string{EventApprove: "APPROVED", EventRequestChanges: "CHANGES_REQUESTED", EventComment: "COMMENTED"}

// SlackLine is the Slack line for a PR, as of a review you just posted with
// event, which the inbox may not show yet ("" for none). A post mentions an
// author that slack_names maps to a Slack user; a copy names them by the
// display name Slack completes @ with when you paste it.
func (co *Core) SlackLine(key string, event ReviewEvent, post bool) string {
	co.mu.Lock()
	it, _ := co.item(key)
	co.mu.Unlock()
	if state := reviewStates[event]; state != "" {
		it.MyLastReview = &gh.Review{State: state}
	}
	login := it.PR.AuthorLogin()
	co.mu.Lock()
	name := co.cfg.SlackNames[login]
	shown := slackName(co.slackUsers, name)
	co.mu.Unlock()
	if shown == name {
		shown = login
	}
	switch {
	case slack.IsUserID(name) && post:
		return slack.Line(it, "<@"+name+">")
	case slack.IsUserID(name):
		return slack.Line(it, "@"+shown)
	}
	return slack.Line(it, cmp.Or(name, login))
}

// ErrNoSlackUser is DMAuthor's answer for an author slack_names doesn't map
// to a Slack user.
var ErrNoSlackUser = errors.New("no Slack user for the author")

// DMAuthor sends the PR's author its Slack line in a direct message.
func (co *Core) DMAuthor(key string) error {
	co.mu.Lock()
	sl, err := co.slackFor()
	it, _ := co.item(key)
	id := co.cfg.SlackNames[it.PR.AuthorLogin()]
	co.mu.Unlock()
	if err != nil {
		return err
	}
	if !slack.IsUserID(id) {
		return ErrNoSlackUser
	}
	if err := sl.DM(context.Background(), id, co.SlackLine(key, "", true)); err != nil {
		return fmt.Errorf("DM %s: %w", it.PR.AuthorLogin(), err)
	}
	return nil
}
