package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tmstorm/prowl/internal/config"
)

// summarySchema is the answer to a summary turn: markdown in summary.
const summarySchema = `{"type":"object","properties":{"summary":{"type":"string","description":"the summary, as markdown"}},"required":["summary"],"additionalProperties":false}`

// diffLimit caps how much of a PR's diff goes into a summary prompt.
const diffLimit = 150_000

// Summarize asks the main reviewer for a plain-language summary of a PR:
// what it does, the notable changes, and what deserves a careful look. It is
// one turn with the diff in the prompt, in an empty directory, so it is quick
// and needs no checkout. It returns the markdown and the model that wrote it.
func Summarize(ctx context.Context, cfg config.Config, j Job, diff string) (string, string, error) {
	be, err := For(cfg, "")
	if err != nil {
		return "", "", err
	}
	dir, err := os.MkdirTemp("", "prowl-summary-")
	if err != nil {
		return "", "", fmt.Errorf("summary dir: %w", err)
	}
	defer os.RemoveAll(dir)
	cut := ""
	if len(diff) > diffLimit {
		diff, cut = diff[:diffLimit], "\n(The diff is cut off here; it is longer.)\n"
	}
	prompt := fmt.Sprintf(`Summarize pull request #%d in %s for a reviewer about to read it.

Title: %s
Author: %s

Description:
"""
%s
"""

Diff:
`+"```diff\n%s\n```"+`%s
Write markdown with three parts, and nothing else:
1. Two or three sentences on what the PR does and why, in plain words. If the description is missing or wrong about something, say what the code actually does.
2. "Notable changes": up to six bullets, each naming the file or area in backticks and what changed there.
3. "Look closely at": up to four bullets on what deserves a careful read: migrations, auth, concurrency, public API or contract changes, deleted tests, anything surprising. Say "nothing stands out" if so.
Be concise. No praise, no review findings, no verdict.
`, j.Number, j.Repo, j.Title, j.Author, j.Body, diff, cut)
	model := cfg.Agent.SummaryModel(be.Name())
	used := model
	res, _, err := be.Review(ctx, Turn{Dir: dir, Prompt: prompt, Model: model, Schema: summarySchema, SetModel: func(m string) { used = m }})
	if err != nil {
		return "", "", fmt.Errorf("summarize %s#%d: %w", j.Repo, j.Number, err)
	}
	return strings.TrimSpace(res.Summary), used, nil
}
