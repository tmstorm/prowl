package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/platform"
	"github.com/tmstorm/prowl/internal/slack"
	"github.com/tmstorm/prowl/internal/store"
)

// UseSlack reads the PR links shared in Slack on each sync, as you.
func (co *Core) UseSlack(c *slack.Client) {
	app := platform.SlackApp()
	co.mu.Lock()
	defer co.mu.Unlock()
	co.slack, co.slackApp = c, app
}

// LoadSlack reads the PR links shared in Slack with the token you saved, when
// you're signed in.
func (co *Core) LoadSlack() error {
	token, err := slack.LoadToken(co.st.SlackSignedIn())
	if err != nil || token.Access == "" {
		return err
	}
	co.UseSlack(slack.New(token))
	return nil
}

// slackLink opens a share's message: in the Slack app when it's installed,
// which goes straight there, else its permalink, which goes through the
// browser. A reply in a thread opens the thread's first message.
func (co *Core) slackLink(s store.SlackShare) string {
	co.mu.Lock()
	app, team := co.slackApp, co.slackTeam
	co.mu.Unlock()
	if !app || team == "" {
		return s.Link
	}
	return "slack://channel?" + url.Values{"team": {team}, "id": {s.Channel}, "message": {cmp.Or(s.ThreadTS, s.TS)}}.Encode()
}

// SlackSignedIn says who you're signed in to Slack as, e.g. "sam in Acme",
// once a sync has asked.
func (co *Core) SlackSignedIn() string {
	co.mu.Lock()
	defer co.mu.Unlock()
	return co.slackWho
}

// SlackErr is why the last sync couldn't read Slack, or nil.
func (co *Core) SlackErr() error {
	co.mu.Lock()
	defer co.mu.Unlock()
	return co.slackErr
}

// slackIndexLag is how long Slack search can take to find a new message.
// Each search reaches this far back past the last one.
const slackIndexLag = 5 * time.Minute

// syncSlack keeps the PR links posted in your channels and DMs since the last
// search, with each PR's node ID and its key as GitHub spells it. A PR shared
// by someone else that prowl hasn't seen shared before is looked up once, and
// returned so this sync shows it. Only a sync, which holds co.syncing, sets
// co.slackMe, so the sync reads it without co.mu.
func (co *Core) syncSlack(ctx context.Context, sl *slack.Client, cfg config.Config, kept []store.SlackShare) ([]gh.PR, error) {
	if co.slackMe == "" {
		id, err := sl.AuthTest(ctx)
		if err != nil {
			return nil, err
		}
		co.mu.Lock()
		co.slackMe, co.slackWho, co.slackTeam = id.UserID, id.User+" in "+id.Team, id.TeamID
		co.mu.Unlock()
		_, _ = co.loadSlackUsers(ctx, sl)
	}
	since := co.st.SlackSearched()
	if since.IsZero() {
		since = time.Now().AddDate(0, 0, -7)
	}
	start := time.Now()
	found, err := sl.Shares(ctx, since)
	if err != nil {
		return nil, err
	}
	known := map[string]store.SlackShare{}
	for _, k := range kept {
		if k.PRID != "" {
			known[strings.ToLower(k.PRKey)] = store.SlackShare{PRKey: k.PRKey, PRID: k.PRID}
		}
	}
	found = slices.DeleteFunc(found, func(s slack.Share) bool { return !watchedShare(cfg, s.Channel, s.DM) })
	var lookup []slack.Share
	for _, s := range found {
		if known[strings.ToLower(s.Key())].PRID == "" && s.User != co.slackMe &&
			!slices.ContainsFunc(lookup, func(l slack.Share) bool { return strings.EqualFold(l.Key(), s.Key()) }) {
			lookup = append(lookup, s)
		}
	}
	looked := make([]*gh.PR, len(lookup))
	var wg sync.WaitGroup
	limit := make(chan struct{}, 8)
	for i, s := range lookup {
		wg.Go(func() {
			limit <- struct{}{}
			defer func() { <-limit }()
			if pr, err := co.gh.PR(ctx, s.Repo, s.Number); err == nil && pr.State == "OPEN" {
				looked[i] = pr
			}
		})
	}
	wg.Wait()
	var prs []gh.PR
	for _, pr := range looked {
		if pr != nil {
			known[strings.ToLower(pr.Key())] = store.SlackShare{PRKey: pr.Key(), PRID: pr.ID}
			prs = append(prs, *pr)
		}
	}
	var save []store.SlackShare
	for _, s := range found {
		pr := known[strings.ToLower(s.Key())]
		if pr.PRID == "" && s.User != co.slackMe {
			continue
		}
		save = append(save, store.SlackShare{Channel: s.Channel, TS: s.TS, PRKey: cmp.Or(pr.PRKey, s.Key()), PRID: pr.PRID, ChannelName: s.Name,
			ThreadTS: s.ThreadTS, User: s.User, UserName: s.UserName, At: s.At, Link: s.Link})
	}
	if err := co.st.SaveSlackShares(save); err != nil {
		return nil, err
	}
	return prs, co.st.SetSlackSearched(start.Add(-slackIndexLag))
}

// watchedShare reports whether a share in this channel is for you: a channel
// you listed, or a DM while DMs count.
func watchedShare(cfg config.Config, channel string, dm bool) bool {
	if dm {
		return cfg.SlackDMs
	}
	return slices.Contains(cfg.SlackChannels, channel)
}

// newestShares is each PR's newest share by someone else, in a channel or DM
// that counts. shares is newest first.
func newestShares(cfg config.Config, me string, shares []store.SlackShare) map[string]store.SlackShare {
	out := map[string]store.SlackShare{}
	for _, s := range shares {
		if s.User == me || s.PRID == "" || !watchedShare(cfg, s.Channel, s.ChannelName == "") {
			continue
		}
		if _, ok := out[s.PRKey]; !ok {
			out[s.PRKey] = s
		}
	}
	return out
}

// pruneShares forgets the shares of PRs that are closed. A share outside
// the inbox may be of a closed PR, so each is asked about.
func (co *Core) pruneShares(ctx context.Context, open map[string]string) {
	shares, err := co.st.SlackShares()
	if err != nil {
		return
	}
	asked := map[string]bool{}
	for _, s := range shares {
		if _, ok := open[s.PRKey]; ok || asked[s.PRKey] {
			continue
		}
		asked[s.PRKey] = true
		repo, n, err := parseKey(s.PRKey)
		if err != nil {
			continue
		}
		if pr, err := co.gh.PR(ctx, repo, n); err == nil && pr.State != "OPEN" {
			_ = co.st.DeleteSlackShares(s.PRKey)
		}
	}
}

// withShares adds the PRs first shared this sync to in, and which PRs are
// shared, and returns each PR's newest share. The shares are read again, so
// the ones saved this sync count.
func (co *Core) withShares(in *gh.Inbox, cfg config.Config, newlyShared []gh.PR) map[string]store.SlackShare {
	have := map[string]bool{}
	for _, pr := range in.PRs {
		have[pr.ID] = true
	}
	for _, pr := range newlyShared {
		if !have[pr.ID] {
			in.PRs = append(in.PRs, pr)
		}
	}
	all, _ := co.st.SlackShares()
	shares := newestShares(cfg, co.slackMe, all)
	in.Shared, in.SlackLinks = map[string]gh.Share{}, map[string]string{}
	for key, s := range shares {
		in.Shared[key] = gh.Share{Channel: s.ChannelName, From: s.UserName, At: s.At}
		in.SlackLinks[key] = co.slackLink(s)
	}
	return shares
}

// reconcileReactions keeps your emoji on each shared PR's newest share in
// step with your review, wherever you made it. Only a share whose emoji
// should change costs Slack calls.
func (co *Core) reconcileReactions(ctx context.Context, sl *slack.Client) error {
	co.mu.Lock()
	cfg, shares, viewer := co.cfg, maps.Clone(co.slackShares), co.inbox.Viewer
	items := slices.Clone(co.inbox.Items)
	co.mu.Unlock()
	if cfg.SlackAfterReview != "react" {
		return nil
	}
	var errs []error
	for _, it := range items {
		s, ok := shares[it.PR.Key()]
		if !ok || it.PR.AuthorLogin() == viewer {
			continue
		}
		if want := co.reaction(cfg, it, viewer, s.At); want != s.MyReaction {
			errs = append(errs, co.setReaction(ctx, sl, s, want))
		}
	}
	return errors.Join(errs...)
}

// reaction is the emoji for where your review of a PR stands since it was
// shared at since: your latest review after it, else reviewing while prowl
// holds an agent review or your inline comments for it, else none. A comment
// review counts when it has a body or starts a thread; replying in someone
// else's thread doesn't.
func (co *Core) reaction(cfg config.Config, it inbox.Item, me string, since time.Time) string {
	var last *gh.Review
	for i, r := range it.PR.Reviews.Nodes {
		if r.Author == nil || r.Author.Login != me || !r.SubmittedAt.After(since) || last != nil && !r.SubmittedAt.After(last.SubmittedAt) {
			continue
		}
		if r.State == "APPROVED" || r.State == "CHANGES_REQUESTED" || r.State == "COMMENTED" && commented(it.PR, r, me) {
			last = &it.PR.Reviews.Nodes[i]
		}
	}
	emoji := cfg.SlackReactions
	if last != nil {
		return map[string]string{"APPROVED": emoji.Approved, "CHANGES_REQUESTED": emoji.ChangesRequested, "COMMENTED": emoji.Commented}[last.State]
	}
	co.mu.Lock()
	defer co.mu.Unlock()
	key := it.PR.Key()
	if len(co.pending[key]) > 0 || slices.ContainsFunc(co.runs[key], func(r *Review) bool {
		return r.StartedAt.After(since) && (r.Status == ReviewRunning || r.Status == ReviewDone)
	}) {
		return emoji.Reviewing
	}
	return ""
}

// commented reports whether a comment review r says something: a body, or a
// thread you started around when it was submitted.
func commented(pr gh.PR, r gh.Review, me string) bool {
	if strings.TrimSpace(r.Body) != "" {
		return true
	}
	for _, t := range pr.ReviewThreads.Nodes {
		if !t.OpenedBy(me) {
			continue
		}
		for _, c := range t.Comments.Nodes {
			if c.Author != nil && c.Author.Login == me && c.CreatedAt.Sub(r.SubmittedAt).Abs() < time.Minute {
				return true
			}
		}
	}
	return false
}

// setReaction swaps the emoji prowl set on a share for want ("" for none).
func (co *Core) setReaction(ctx context.Context, sl *slack.Client, s store.SlackShare, want string) error {
	if s.MyReaction != "" {
		if err := sl.Unreact(ctx, s.Channel, s.TS, s.MyReaction); err != nil {
			return err
		}
	}
	if want != "" {
		if err := sl.React(ctx, s.Channel, s.TS, want); err != nil {
			want = ""
			_ = co.st.SetSlackReaction(s, want)
			return err
		}
	}
	if err := co.st.SetSlackReaction(s, want); err != nil {
		return err
	}
	co.mu.Lock()
	if cur, ok := co.slackShares[s.PRKey]; ok && cur.Channel == s.Channel && cur.TS == s.TS {
		cur.MyReaction = want
		co.slackShares[s.PRKey] = cur
	}
	co.mu.Unlock()
	return nil
}

// afterReview tells the newest Slack share of the PR about the review you
// just posted, as slack_after_review says, and says what it did: "" when
// the PR has no share or Slack isn't connected.
func (co *Core) afterReview(ctx context.Context, key string, event ReviewEvent) (string, error) {
	co.mu.Lock()
	sl, cfg := co.slack, co.cfg
	loggedOut := co.authErr("slack")
	s, shared := co.slackShares[key]
	co.mu.Unlock()
	state := reviewStates[event]
	if sl == nil || !shared || state == "" || cfg.SlackAfterReview == "off" {
		return "", nil
	}
	if loggedOut != nil {
		return "", loggedOut
	}
	where := place(s)
	switch cfg.SlackAfterReview {
	case "react":
		emoji := map[string]string{"APPROVED": cfg.SlackReactions.Approved, "CHANGES_REQUESTED": cfg.SlackReactions.ChangesRequested,
			"COMMENTED": cfg.SlackReactions.Commented}[state]
		note := "reacted :" + emoji + ": in " + where
		if emoji == s.MyReaction {
			return note, nil
		}
		return note, co.setReaction(ctx, sl, s, emoji)
	case "reply":
		return "replied in " + where, sl.Post(ctx, s.Channel, cmp.Or(s.ThreadTS, s.TS), co.SlackLine(key, event, true))
	}
	return "", nil
}

// SharedIn is where the PR's newest Slack share is, e.g. "#reviews" or
// "DM", or "" when it has none or Slack isn't connected.
func (co *Core) SharedIn(key string) string {
	co.mu.Lock()
	defer co.mu.Unlock()
	s, ok := co.slackShares[key]
	if co.slack == nil || !ok {
		return ""
	}
	return place(s)
}

func place(s store.SlackShare) string {
	if s.ChannelName == "" {
		return "DM"
	}
	return "#" + s.ChannelName
}

// PostInThread posts text in the thread of the PR's newest Slack share and
// says where.
func (co *Core) PostInThread(ctx context.Context, key, text string) (string, error) {
	co.mu.Lock()
	sl, err := co.slackFor()
	s, ok := co.slackShares[key]
	co.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("%s has no Slack share to post in", key)
	}
	if err != nil {
		return "", err
	}
	if err := sl.Post(ctx, s.Channel, cmp.Or(s.ThreadTS, s.TS), text); err != nil {
		return "", fmt.Errorf("post in %s: %w", place(s), err)
	}
	return place(s), nil
}

// reactionsOnYours reads the emoji on the newest share of each of your open
// PRs, whoever posted it, and sums up each one, e.g. "#reviews: Bob ✅,
// Carol 👀". Only the emoji in slack_reactions count, and not yours; someone
// with several counts by the one that says most. One call per shared PR.
// It adds the link to each share to in.SlackLinks.
func (co *Core) reactionsOnYours(ctx context.Context, sl *slack.Client, cfg config.Config, in *gh.Inbox) (map[string]string, error) {
	yours := map[string]string{}
	for _, pr := range in.PRs {
		if pr.AuthorLogin() == in.Viewer {
			yours[strings.ToLower(pr.Key())] = pr.Key()
		}
	}
	all, err := co.st.SlackShares()
	if err != nil {
		return nil, err
	}
	newest := map[string]store.SlackShare{}
	for _, s := range all {
		key, ok := yours[strings.ToLower(s.PRKey)]
		if _, seen := newest[key]; ok && !seen && watchedShare(cfg, s.Channel, s.ChannelName == "") {
			newest[key] = s
			in.SlackLinks[key] = co.slackLink(s)
		}
	}
	e := cfg.SlackReactions
	rank := map[string]int{e.Reviewing: 1, e.Commented: 2, e.Approved: 3, e.ChangesRequested: 4}
	symbol := []string{1: "👀", 2: "💬", 3: "✅", 4: "❌"}
	co.mu.Lock()
	users := co.slackUsers
	co.mu.Unlock()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errs []error
	out := map[string]string{}
	for key, s := range newest {
		wg.Go(func() {
			reactions, err := sl.Reactions(ctx, s.Channel, s.TS)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			best := map[string]int{}
			for emoji, who := range reactions {
				for _, u := range who {
					if u != co.slackMe && rank[emoji] > best[u] {
						best[u] = rank[emoji]
					}
				}
			}
			ids := slices.SortedFunc(maps.Keys(best), func(a, b string) int {
				return cmp.Or(cmp.Compare(best[b], best[a]), cmp.Compare(slackName(users, a), slackName(users, b)))
			})
			var names []string
			for _, u := range ids {
				names = append(names, slackName(users, u)+" "+symbol[best[u]])
			}
			if len(names) > 0 {
				out[key] = place(s) + ": " + strings.Join(names, ", ")
			}
		})
	}
	wg.Wait()
	return out, errors.Join(errs...)
}

// slackName is how Slack shows a person: their display name, else their
// full name or handle.
func slackName(users []slack.User, id string) string {
	i := slices.IndexFunc(users, func(u slack.User) bool { return u.ID == id })
	if i < 0 {
		return id
	}
	return cmp.Or(users[i].Display, users[i].RealName, users[i].Handle)
}

// ChannelActivity is a channel you're in and how many PR links of your orgs
// were posted in it lately.
type ChannelActivity struct {
	ID, Name    string
	Private     bool
	Links       int
	Recommended bool // enough links to be worth watching
}

// Recent PR links decide which channels prowl recommends: at least
// recommendLinks in the last activityWindow.
const (
	activityWindow = 14 * 24 * time.Hour
	recommendLinks = 5
)

// SlackActivity lists the channels you're in, the busiest with PR links of
// your orgs first, and how many such links reached you in DMs. It searches
// Slack once per session.
func (co *Core) SlackActivity(ctx context.Context) ([]ChannelActivity, int, error) {
	co.mu.Lock()
	sl, err := co.slackFor()
	cfg, cached, dms := co.cfg, co.activity, co.dmLinks
	co.mu.Unlock()
	if err != nil {
		return nil, 0, err
	}
	if cached != nil {
		return cached, dms, nil
	}
	channels, err := sl.Channels(ctx)
	if err != nil {
		return nil, 0, err
	}
	shares, err := sl.Shares(ctx, time.Now().Add(-activityWindow))
	if err != nil {
		return nil, 0, err
	}
	links := map[string]int{}
	dms = 0
	for _, s := range shares {
		owner, _, _ := strings.Cut(s.Repo, "/")
		if len(cfg.Orgs) > 0 && !slices.ContainsFunc(cfg.Orgs, func(o string) bool { return strings.EqualFold(o, owner) }) {
			continue
		}
		if s.DM {
			dms++
		} else {
			links[s.Channel]++
		}
	}
	out := make([]ChannelActivity, 0, len(channels))
	for _, ch := range channels {
		out = append(out, ChannelActivity{ID: ch.ID, Name: ch.Name, Private: ch.Private, Links: links[ch.ID], Recommended: links[ch.ID] >= recommendLinks})
	}
	slices.SortStableFunc(out, func(a, b ChannelActivity) int {
		return cmp.Or(cmp.Compare(b.Links, a.Links), cmp.Compare(a.Name, b.Name))
	})
	co.mu.Lock()
	co.activity, co.dmLinks = out, dms
	co.mu.Unlock()
	return out, dms, nil
}
