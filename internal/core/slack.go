package core

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/slack"
	"github.com/tmstorm/prowl/internal/store"
)

// UseSlack reads the PR links shared in Slack on each sync, as you.
func (co *Core) UseSlack(c *slack.Client) {
	co.mu.Lock()
	defer co.mu.Unlock()
	co.slack = c
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
// returned so this sync shows it. The caller holds co.syncing, which guards
// co.slackMe.
func (co *Core) syncSlack(sl *slack.Client, cfg config.Config, kept []store.SlackShare) ([]gh.PR, error) {
	ctx := context.Background()
	if co.slackMe == "" {
		id, err := sl.AuthTest(ctx)
		if err != nil {
			return nil, err
		}
		co.slackMe = id.UserID
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
			if pr, err := co.gh.PR(s.Repo, s.Number); err == nil && pr.State == "OPEN" {
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
			ThreadTS: s.ThreadTS, User: s.User, UserName: s.UserName, At: s.At})
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

// shared is each PR's newest share by someone else, in a channel or DM that
// counts, and the node IDs of those PRs. shares is newest first.
func shared(cfg config.Config, me string, shares []store.SlackShare) (map[string]gh.Share, []string) {
	out := map[string]gh.Share{}
	var ids []string
	for _, s := range shares {
		if s.User == me || s.PRID == "" || !watchedShare(cfg, s.Channel, s.ChannelName == "") {
			continue
		}
		if _, ok := out[s.PRKey]; !ok {
			out[s.PRKey] = gh.Share{Channel: s.ChannelName, From: s.UserName, At: s.At}
			ids = append(ids, s.PRID)
		}
	}
	return out, ids
}

// pruneShares forgets the shares of PRs that are closed. A share outside
// the inbox may be of a closed PR, so each is asked about.
func (co *Core) pruneShares(open map[string]string) {
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
		if pr, err := co.gh.PR(repo, n); err == nil && pr.State != "OPEN" {
			_ = co.st.DeleteSlackShares(s.PRKey)
		}
	}
}

// withShares adds the PRs first shared this sync to in, and which PRs are
// shared. The shares are read again, so the ones saved this sync count.
func (co *Core) withShares(in *gh.Inbox, cfg config.Config, newlyShared []gh.PR) {
	have := map[string]bool{}
	for _, pr := range in.PRs {
		have[pr.ID] = true
	}
	for _, pr := range newlyShared {
		if !have[pr.ID] {
			in.PRs = append(in.PRs, pr)
		}
	}
	shares, _ := co.st.SlackShares()
	in.Shared, _ = shared(cfg, co.slackMe, shares)
}
