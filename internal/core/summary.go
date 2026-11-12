package core

import (
	"fmt"
	"slices"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/store"
)

// autoSummaryLimit is how many summaries auto_summary writes at once.
const autoSummaryLimit = 3

// Summary is a PR's AI summary, or nil when it has none.
func (co *Core) Summary(key string) *store.Summary {
	co.mu.Lock()
	defer co.mu.Unlock()
	if sum, ok := co.summaries[key]; ok {
		return &sum
	}
	return nil
}

// Summarizing lists the PRs whose summary is being written.
func (co *Core) Summarizing() []string {
	co.mu.Lock()
	defer co.mu.Unlock()
	var keys []string
	for key := range co.summarizing {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// Summarize has the main agent write a plain-language summary of a PR and
// keeps it. It takes seconds; a UI calls it off its own loop.
func (co *Core) Summarize(key string) (store.Summary, error) {
	it, cfg, err := co.beginSummary(key)
	if err != nil {
		return store.Summary{}, err
	}
	return co.finishSummary(it, cfg)
}

// beginSummary marks a summary as being written.
func (co *Core) beginSummary(key string) (inbox.Item, config.Config, error) {
	co.mu.Lock()
	defer co.mu.Unlock()
	it, ok := co.item(key)
	switch {
	case !ok:
		return it, co.cfg, fmt.Errorf("summarize %s: not in the inbox", key)
	case co.summarizing[key]:
		return it, co.cfg, fmt.Errorf("already writing the summary of %s", key)
	}
	if err := co.authErr(co.cfg.Agent.Main()); err != nil {
		return it, co.cfg, err
	}
	if co.summarizing == nil {
		co.summarizing = map[string]bool{}
	}
	co.summarizing[key] = true
	return it, co.cfg, nil
}

func (co *Core) finishSummary(it inbox.Item, cfg config.Config) (store.Summary, error) {
	pr := it.PR
	key := pr.Key()
	sum, err := co.writeSummary(it, cfg)
	co.mu.Lock()
	defer co.mu.Unlock()
	delete(co.summarizing, key)
	if err != nil {
		if co.summaryFailed == nil {
			co.summaryFailed = map[string]string{}
		}
		co.summaryFailed[key] = pr.HeadRefOid
		return store.Summary{}, err
	}
	delete(co.summaryFailed, key)
	co.summaries[key] = sum
	return sum, nil
}

func (co *Core) writeSummary(it inbox.Item, cfg config.Config) (store.Summary, error) {
	be, err := agent.For(cfg, "")
	if err == nil {
		err = be.Check()
	}
	if err != nil {
		return store.Summary{}, err
	}
	pr := it.PR
	key := pr.Key()
	diff, err := co.Diff(key, false)
	if err != nil {
		return store.Summary{}, fmt.Errorf("summarize %s: %w", key, err)
	}
	job := agent.Job{Repo: pr.Repository.NameWithOwner, Number: pr.Number, Title: pr.Title, Author: pr.AuthorLogin(), Body: pr.Body}
	text, model, err := agent.Summarize(cfg, job, diff)
	if err != nil {
		return store.Summary{}, fmt.Errorf("summarize %s: %w", key, err)
	}
	sum := store.Summary{Key: key, HeadOid: pr.HeadRefOid, Text: text, Model: model, At: time.Now()}
	if err := co.st.SaveSummary(sum); err != nil {
		return store.Summary{}, err
	}
	return sum, nil
}

// autoSummaries starts summaries, with auto_summary on, for PRs that need
// you and have none, a few at a time. Each reports a SummaryChanged event.
// A summary that failed isn't tried again until the PR is pushed to.
func (co *Core) autoSummaries() {
	if co.loggedIn(co.Config().Agent.Main()) != nil {
		return
	}
	co.mu.Lock()
	var next []string
	if co.cfg.Agent.AutoSummary {
		for _, it := range co.inbox.Items {
			key := it.PR.Key()
			if len(co.summarizing)+len(next) >= autoSummaryLimit {
				break
			}
			_, done := co.summaries[key]
			if done || !it.Bucket.NeedsMe() || co.summarizing[key] || co.summaryFailed[key] == it.PR.HeadRefOid {
				continue
			}
			next = append(next, key)
		}
	}
	co.mu.Unlock()
	for _, key := range next {
		it, cfg, err := co.beginSummary(key)
		if err != nil {
			continue
		}
		go func() {
			_, err := co.finishSummary(it, cfg)
			co.emit(Event{Kind: SummaryChanged, Key: key, Err: err})
		}()
	}
}
