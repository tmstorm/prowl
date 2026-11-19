package agent

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/gh"
)

// Activity is what has been raised on a PR, read once from its page. It is
// both what an AI reviewer reads as the PR's history before reviewing, and
// what the PR's author works through (core.ReviewsOnGitHub), so a source of
// review information added here reaches both.
type Activity struct {
	Reviews  []Remark // oldest first, pending ones left out
	Comments []Remark // the conversation, oldest first
	Threads  []gh.Thread
	Checks   []gh.Check // failing, or with annotations
	bots     map[string]bool
}

// Remark is a review or a comment on a PR.
type Remark struct {
	Author string
	Bot    bool // an app, Actions, a [bot] login, or in the bots setting
	At     time.Time
	// State is a review's state, or "" for a comment. A dismissed review
	// keeps what it was in Was.
	State, Was string
	Commit     string // the commit a review was on
	Body       string
	// Superseded is a bot's review or comment that the same bot followed
	// with a later one: what it raised is settled.
	Superseded bool
}

// conversationWindow is how many of the latest conversation comments count:
// older ones are answered or stale.
const conversationWindow = 15

// ActivityOf reads a PR's page. Bots are what IsBot says with the bots
// setting, and only each bot's latest review or comment stands.
func ActivityOf(det *gh.Detail, bots map[string]bool) Activity {
	a := Activity{Threads: det.Threads, bots: bots}
	latestBot := map[string]time.Time{}
	for _, r := range det.Reviews {
		if r.Author.IsBot(bots) && r.SubmittedAt.After(latestBot[r.Author.Name()]) {
			latestBot[r.Author.Name()] = r.SubmittedAt
		}
	}
	for _, cm := range det.Comments {
		if cm.Author.IsBot(bots) && cm.CreatedAt.After(latestBot[cm.Author.Name()]) {
			latestBot[cm.Author.Name()] = cm.CreatedAt
		}
	}
	superseded := func(who *gh.Actor, at time.Time) bool {
		return who.IsBot(bots) && at.Before(latestBot[who.Name()])
	}
	for _, r := range det.Reviews {
		if r.State == "PENDING" {
			continue
		}
		m := Remark{Author: r.Author.Name(), Bot: r.Author.IsBot(bots), At: r.SubmittedAt, State: r.State, Was: r.PreviousState,
			Body: r.Body, Superseded: superseded(r.Author, r.SubmittedAt)}
		if r.Commit != nil {
			m.Commit = r.Commit.Oid
		}
		a.Reviews = append(a.Reviews, m)
	}
	for _, cm := range det.Comments {
		a.Comments = append(a.Comments, Remark{Author: cm.Author.Name(), Bot: cm.Author.IsBot(bots), At: cm.CreatedAt, Body: cm.Body,
			Superseded: superseded(cm.Author, cm.CreatedAt)})
	}
	for _, c := range det.Checks {
		if c.State == "FAIL" || len(c.Annotations) > 0 {
			a.Checks = append(a.Checks, c)
		}
	}
	return a
}

// Conversation is the latest comments, the ones that count.
func (a Activity) Conversation() []Remark {
	return a.Comments[max(0, len(a.Comments)-conversationWindow):]
}

// Bot is whether who is a bot, by the same rule as the rest of the activity.
func (a Activity) Bot(who *gh.Actor) bool { return who.IsBot(a.bots) }

// FindingMarker tags a finding prowl posts, hidden on GitHub, so the PR
// author's prowl can tell it from a hand-written comment and read its
// severity and rule (ParseFinding).
func FindingMarker(severity, rule string) string {
	m := "<!-- prowl:finding v=1 severity=" + severity
	for strings.Contains(rule, "--") {
		rule = strings.ReplaceAll(rule, "--", "-")
	}
	if rule != "" {
		m += " rule=" + strconv.Quote(rule)
	}
	return m + " -->"
}

var (
	markerRe = regexp.MustCompile(`<!-- prowl:finding v=1 severity=([a-z-]+)(?: rule=("(?:[^"\\]|\\.)*"))? -->`)
	// legacyRe reads a finding prowl posted before the marker: its body
	// starts with the severity in bold.
	legacyRe = regexp.MustCompile(`^\*\*(must-fix|should-fix|nit):\*\* `)
)

// HasFindingMarker is whether s carries the marker of a finding prowl posted.
func HasFindingMarker(s string) bool { return markerRe.MatchString(s) }

// ParseFinding reads a comment prowl posted as a finding: its severity and
// rule, and the text without the marker. ok is false for anything else.
func ParseFinding(body string) (severity, rule, text string, ok bool) {
	if m := markerRe.FindStringSubmatchIndex(body); m != nil {
		if m[4] >= 0 {
			rule, _ = strconv.Unquote(body[m[4]:m[5]])
		}
		return body[m[2]:m[3]], rule, strings.TrimSpace(body[:m[0]] + body[m[1]:]), true
	}
	if m := legacyRe.FindStringSubmatch(strings.TrimSpace(body)); m != nil {
		return m[1], "", strings.TrimSpace(body), true
	}
	return "", "", body, false
}
