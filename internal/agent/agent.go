// Package agent runs headless coding-agent reviews of a PR (Claude Code,
// Codex, or any command) in a dedicated git worktree. Jobs run as detached
// `prowl agent-run` processes so they survive the TUI exiting; progress and
// results are written to the store.
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/platform"
	"github.com/tmstorm/prowl/internal/store"
)

type Job struct {
	Repo    string // owner/name
	Number  int
	Title   string
	Author  string
	Base    string
	Head    string // head oid
	Body    string
	Refine  string // non-empty: follow-up instruction for an existing session
	Viewer  string // the reviewer's GitHub login
	Sync    bool   // Refine is a sync after an interactive chat, not a user instruction
	Agent   string // backend name; empty means the main reviewer
	Profile string // review profile, "" for a plain review
	RunID   int64  // the run this job writes to (Launch sets it)
	// A local review: the checkout to review in place and its branch, before
	// a pull request exists. Number is 0 and Base is the default branch.
	Dir    string
	Branch string
}

// SyncInstruction asks the review session for its current review after the
// reviewer talked to it interactively.
const SyncInstruction = "The reviewer just discussed this review with you in an interactive chat. " +
	"Return the complete review as it stands after that conversation: apply anything you agreed to add, drop, or reword. " +
	"In `note`, say what changed in one or two sentences."

// WorktreePath is where a reviewer's worktree for a PR lives. Each reviewer
// (agent, or agent:profile) has its own, so reviews running at once don't
// share a checkout.
func WorktreePath(repo string, number int, reviewer string) string {
	return filepath.Join(prWorktrees(repo, number), strings.ReplaceAll(reviewer, ":", "-"))
}

// prWorktrees is the directory holding every reviewer's worktree for a PR.
func prWorktrees(repo string, number int) string {
	owner, name, _ := strings.Cut(repo, "/")
	return filepath.Join(config.DataDir(), "worktrees", owner, name, strconv.Itoa(number))
}

func (j Job) Key() string {
	if j.Dir != "" {
		return store.LocalKey(j.Repo, j.Branch)
	}
	return fmt.Sprintf("%s#%d", j.Repo, j.Number)
}

func logDir() string { return filepath.Join(config.DataDir(), "logs") }

func LogPath(key string) string {
	return filepath.Join(logDir(), strings.NewReplacer("/", "_", "#", "_", ":", "_").Replace(key)+".log")
}

// Launch starts a detached `prowl agent-run` process for the job. The job
// goes in a file, not the command line: a PR description can be longer than
// a command line allows, and the process list shows command lines.
func Launch(st *store.Store, j Job) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("launch: %w", err)
	}
	_ = os.MkdirAll(logDir(), 0o700)
	logf, err := os.Create(LogPath(j.Key() + "-" + store.Reviewer(j.Agent, j.Profile)))
	if err != nil {
		return fmt.Errorf("launch: %w", err)
	}
	defer logf.Close()

	if j.Refine != "" && j.RunID != 0 {
		err = st.RestartRun(j.RunID, 0)
	} else {
		j.RunID, err = st.StartRun(j.Key(), j.Head, j.Agent, j.Profile, 0)
	}
	if err != nil {
		return fmt.Errorf("launch: %w", err)
	}
	payload, _ := json.Marshal(j)
	jobFile := filepath.Join(config.DataDir(), "jobs", fmt.Sprintf("%d.json", j.RunID))
	if err := os.MkdirAll(filepath.Dir(jobFile), 0o700); err != nil {
		_ = st.FailReview(j.RunID, err.Error())
		return fmt.Errorf("launch: %w", err)
	}
	if err := os.WriteFile(jobFile, payload, 0o600); err != nil {
		_ = st.FailReview(j.RunID, err.Error())
		return fmt.Errorf("launch: %w", err)
	}
	cmd := exec.Command(exe, "agent-run", jobFile)
	cmd.Stdout, cmd.Stderr = logf, logf
	platform.Detach(cmd)
	if err := cmd.Start(); err != nil {
		_ = st.FailReview(j.RunID, err.Error())
		return fmt.Errorf("start review job: %w", err)
	}
	_ = st.SetReviewPID(j.RunID, cmd.Process.Pid)
	return cmd.Process.Release()
}

// Schema is the JSON Schema a review with the given severities must match.
// Every property is required and no others are allowed, which strict
// structured-output modes need.
func Schema(severities []string) string {
	enum, _ := json.Marshal(severities)
	return strings.Replace(reviewSchema, "SEVERITIES", string(enum), 1)
}

const reviewSchema = `{
  "type": "object",
  "properties": {
    "summary": {"type": "string", "description": "2-5 sentence overall assessment in markdown"},
    "verdict": {"type": "string", "enum": ["approve", "request_changes", "comment"]},
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "severity": {"type": "string", "enum": SEVERITIES},
          "path": {"type": "string", "description": "file path relative to repo root; empty string for findings about the PR itself (description, process)"},
          "line": {"type": "integer", "description": "line number in the NEW version of the file, must be a line changed or shown in the diff; 0 when path is empty"},
          "title": {"type": "string", "description": "one-line summary"},
          "body": {"type": "string", "description": "the comment as posted on GitHub: at most three short sentences saying what is wrong, why it matters, and the fix"},
          "rule": {"type": "string", "description": "if the finding enforces a repo guideline or skill, cite it, e.g. 'CONTRIBUTING.md: Tests'. Empty otherwise."}
        },
        "required": ["severity", "path", "line", "title", "body", "rule"],
        "additionalProperties": false
      }
    },
    "note": {"type": "string", "description": "to the reader only, never posted: what you couldn't check and why it matters, doubts about your review, questions for them, and after an instruction from them, what you changed and why. Empty when there is nothing."},
    "pr_action": {"type": "string", "enum": ["none", "draft", "close"], "description": "what the reader should do with the PR besides reviewing it; almost always none"},
    "pr_action_reason": {"type": "string", "description": "for draft or close: the comment the reader would post, as the reader; empty for none"},
    "points": {
      "type": "array",
      "description": "every unsettled point a reviewer, bot or check raised, checked against the code at HEAD; for the reader only, never posted",
      "items": {
        "type": "object",
        "properties": {
          "who": {"type": "string", "description": "the login that raised it, or the check's name"},
          "path": {"type": "string", "description": "file it applies to; empty for the PR as a whole"},
          "line": {"type": "integer", "description": "line in the new version; 0 when path is empty or no line applies"},
          "point": {"type": "string", "description": "what was raised, in one short sentence"},
          "verdict": {"type": "string", "enum": ["addressed", "open", "wrong", "unclear"]},
          "why": {"type": "string", "description": "one or two sentences of evidence: what changed, or why it still holds or is wrong"}
        },
        "required": ["who", "path", "line", "point", "verdict", "why"],
        "additionalProperties": false
      }
    },
    "thread_replies": {
      "type": "array",
      "description": "one entry per thread waiting on the reader: the reply to send, or an empty body when there is nothing to say back",
      "items": {
        "type": "object",
        "properties": {
          "thread_id": {"type": "string", "description": "the id of a thread listed as waiting on the reader"},
          "body": {"type": "string", "description": "the reply, written as the reader, ready to post; empty for no reply"}
        },
        "required": ["thread_id", "body"],
        "additionalProperties": false
      }
    },
    "guidelines_read": {"type": "array", "items": {"type": "string"}, "description": "guideline files you actually read, as paths"},
    "skills_used": {"type": "array", "items": {"type": "string"}, "description": "skills you used"}
  },
  "required": ["summary", "verdict", "findings", "note", "pr_action", "pr_action_reason", "points", "thread_replies", "guidelines_read", "skills_used"],
  "additionalProperties": false
}`

// guidelineNames are files that tell contributors (and agents) how work is
// done in a repo. They are looked for at the root and in every directory the
// PR touches, nearest last.
var guidelineNames = []string{"AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md", "REVIEWING.md", "CODE_REVIEW.md", "STYLE.md"}
var rootOnly = []string{".github/CONTRIBUTING.md", "docs/CONTRIBUTING.md", ".github/pull_request_template.md", ".github/PULL_REQUEST_TEMPLATE.md"}

func findGuidelines(wt string, changed []string) []string {
	dirs := map[string]bool{".": true}
	for _, f := range changed {
		for d := filepath.Dir(f); d != "." && d != "/" && d != ""; d = filepath.Dir(d) {
			dirs[d] = true
		}
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, k int) bool {
		di, dk := strings.Count(ordered[i], "/"), strings.Count(ordered[k], "/")
		if ordered[i] == "." {
			return true
		}
		if ordered[k] == "." {
			return false
		}
		if di != dk {
			return di < dk
		}
		return ordered[i] < ordered[k]
	})
	var found []string
	exists := func(rel string) bool {
		st, err := os.Stat(filepath.Join(wt, rel))
		return err == nil && !st.IsDir()
	}
	for _, d := range ordered {
		for _, n := range guidelineNames {
			rel := filepath.Join(d, n)
			if exists(rel) {
				found = append(found, rel)
			}
		}
		if d == "." {
			for _, rel := range rootOnly {
				if exists(rel) {
					found = append(found, rel)
				}
			}
		}
	}
	return found
}

func prompt(j Job, cfg config.Config, be Backend, guidelines []string, h history, dismissed, examples string) string {
	reader := "the reviewer"
	if j.Viewer != "" {
		reader = j.Viewer
	}
	var b strings.Builder
	if j.Dir != "" {
		b.WriteString(localHeader(j, reader, h, localCommands(cfg, be, j)))
	} else {
		fmt.Fprintf(&b, `You are reviewing pull request #%d in %s for %s, who will read your review before anything is posted.

Title: %s
Author: %s
Base branch: %s

PR description:
"""
%s
"""

The PR head is checked out in the current directory. See the change with:
  git diff origin/%s...HEAD
  git log origin/%s..HEAD

`, j.Number, j.Repo, reader, j.Title, j.Author, j.Base, j.Body, j.Base, j.Base)
	}

	if h.text != "" {
		b.WriteString("## Review history\n\nThis is what is already on the PR page. Read it before reviewing, the way a reviewer would.\n\n")
		b.WriteString(h.text + "\n")
	}
	if rr := reReviewSection(j, h); rr != "" {
		b.WriteString(rr + "\n")
	}
	b.WriteString(dismissed)
	b.WriteString(examples)
	if len(guidelines) > 0 {
		b.WriteString("## Repo guidelines (required)\n\n")
		b.WriteString("This repo documents how work must be done. Before reviewing, read every file\n")
		b.WriteString("below in full, then follow what they link to that is relevant to this change\n")
		b.WriteString("(contribution standards, testing, documentation, commit and PR rules). Hold\n")
		b.WriteString("the PR to them: a violation of a documented rule is a finding, and its\n")
		b.WriteString("`rule` field must cite the file and section. Files nearer a changed file\n")
		b.WriteString("take precedence for that file.\n\n")
		for _, g := range guidelines {
			b.WriteString("- " + g + "\n")
		}
		b.WriteString("\n")
	}
	if dirs := cfg.ExpandedContextDirs(); len(dirs) > 0 {
		owner, _, _ := strings.Cut(j.Repo, "/")
		b.WriteString("## Linked standards\n\nGuidelines may link to other repos on GitHub. These are cloned locally and\nreadable; read the local copy instead of the URL:\n\n")
		for _, d := range dirs {
			if st, err := os.Stat(d); err != nil || !st.IsDir() {
				continue
			}
			fmt.Fprintf(&b, "- https://github.com/%s/%s/blob/<any-ref>/<path>  ->  %s/<path>\n", owner, filepath.Base(d), d)
		}
		b.WriteString("\n")
	}
	if len(h.waiting) > 0 {
		b.WriteString(replySection)
	}
	skills := cfg.Agent.Skills
	profile, focused := cfg.Agent.Profile(j.Profile)
	if focused {
		skills = append(append([]string{}, skills...), profile.Skills...)
	}
	b.WriteString(skillsSection(be, skills))

	b.WriteString("## How to review\n\n" + strings.TrimSpace(cfg.Agent.Instructions()) + "\n\n")
	b.WriteString(severitySection(cfg.Agent.Severities()))
	if j.Dir == "" {
		b.WriteString(pointsSection)
		b.WriteString(actionSection)
	}
	b.WriteString(`## Output rules

- Do not repeat as a finding anything a reviewer, a bot or a check already
  raised; check it in ` + "`points`" + ` instead.
- Code findings point at a line in the new version of a file in the diff.
  Findings about the PR itself (description, missing sections, process) use an
  empty path and line 0.
- ` + "`summary`" + `, finding bodies, ` + "`pr_action_reason`" + ` and thread replies are posted under
  the reader's name. Write them as the reader, to the author and reviewers.
  Nothing addressed to the reader or about this session goes in them.
- Anything for the reader alone goes in ` + "`note`" + `: what you couldn't check here
  (an app's permissions, repo settings, another repo) and why it matters,
  doubts about your own review, questions for them. This session is read-only
  and may not reach GitHub; don't mention commands that were refused.
- List the guideline files you actually read in ` + "`guidelines_read`" + ` and
  the skills you used in ` + "`skills_used`" + `.
`)
	if focused {
		fmt.Fprintf(&b, "\n## Review focus: %s\n\nThis review has a focus. Where it differs from the instructions above, follow the focus.\n\n%s\n",
			profile.Name, strings.TrimSpace(profile.Prompt))
	}
	return b.String()
}

// pointsSection asks the agent to check what others already raised.
const pointsSection = `## Points raised

Check every point a reviewer (the reader included), a bot or a check has
raised on this PR and that is not settled, against the code at HEAD, and
list it in ` + "`points`" + `. Points are: unresolved review threads, review
bodies that ask for a change, comments that make a claim about the code, and
check output. Resolved threads are settled and stay out, as do comments that
make no claim about the code (approvals, questions about timing). For each:
- verdict addressed: the code now handles it; say what changed. A point that
  held on the commit it was made on and no longer does is addressed.
- verdict open: it still holds at HEAD.
- verdict wrong: the claim never matched the code; say why.
- verdict unclear: you can't tell from the code.
An automated reviewer that ran again speaks through its latest run only:
leave out points from its runs marked superseded.
These are for the reader only and never posted; don't argue with a reviewer
anywhere else. A point that is still open and must-fix may also be a finding
that cites who raised it.

`

// actionSection lets the agent suggest closing the PR or making it a draft.
const actionSection = `## Closing or drafting

Almost every PR should be reviewed as it is: set ` + "`pr_action`" + ` to none. Suggest
close only when the PR shouldn't be merged in any form: other work supersedes
or duplicates it, the approach is wrong for this codebase, or it has been
abandoned. Suggest draft when it isn't ready for review yet: the author says
it is in progress, required parts are missing, or it doesn't build. For
either, write ` + "`pr_action_reason`" + ` as the comment the reader would post when
doing it, as the reader. The reader decides; never say in the summary that the
PR will be closed.

`

// replySection asks for draft replies to the threads waiting on the reader.
const replySection = `## Replies to threads

The threads under "Threads waiting on" above are ones where someone answered
the reader and the reader has not responded yet. Go through every one, and
put one entry per thread in ` + "`thread_replies`" + ` with its id:
- The point was fixed or answered, and it holds up when you check the code:
  draft a short reply that says it is settled and names what settles it,
  e.g. "f96ac28 covers it."
- The answer raises something new, or only partly settles the point: draft
  the reply that answers it, or asks the one question still open.
- There is nothing to say back (the reader already answered elsewhere, or
  the last comment needs no answer): leave ` + "`body`" + ` empty.
A resolved thread still gets a reply when the author answered the reader:
they are often waiting for the reader to agree. Saying it in the summary
does not replace the reply in the thread. Write as the reader, first
person, the way they write comments. Never raise a resolved point again as
a finding; reply in its thread instead.

`

var severityMeaning = map[string]string{
	"must-fix":   "a bug, security issue, data loss, broken contract, or a violation of a rule the guidelines say is required; blocks the merge",
	"should-fix": "a real problem that doesn't block the merge",
	"nit":        "minor and optional",
}

// severitySection defines the severities a finding may have. The reader
// turned the others off, so the agent leaves out findings below them.
func severitySection(severities []string) string {
	var b strings.Builder
	b.WriteString("## Severity\n\nEvery finding has one of these severities:\n")
	for _, s := range severities {
		fmt.Fprintf(&b, "- %s: %s\n", s, severityMeaning[s])
	}
	if len(severities) < len(severityMeaning) {
		b.WriteString("\nThe reader doesn't want anything less severe. Leave it out of the findings\nand the summary.\n")
	}
	return b.String() + "\n"
}

// allowed drops findings whose severity the reader turned off, in case the
// agent ignored the schema.
func allowed(fs []store.Finding, severities []string) []store.Finding {
	return slices.DeleteFunc(fs, func(f store.Finding) bool { return !slices.Contains(severities, f.Severity) })
}

type result struct {
	Note          string        `json:"note"`
	Points        []store.Point `json:"points"`
	PRAction      string        `json:"pr_action"`
	PRReason      string        `json:"pr_action_reason"`
	ThreadReplies []struct {
		ThreadID string `json:"thread_id"`
		Body     string `json:"body"`
	} `json:"thread_replies"`
	Summary        string          `json:"summary"`
	Verdict        string          `json:"verdict"`
	Findings       []store.Finding `json:"findings"`
	GuidelinesRead []string        `json:"guidelines_read"`
	SkillsUsed     []string        `json:"skills_used"`
}

// Run executes the job in the foreground (called inside the detached process).
// c reads the PR's history from GitHub; nil leaves the history out.
// A revise or sync runs on the agent that made the review, since a session
// only resumes there. A failure from a logged-out agent is reported as that,
// whatever the agent printed.
func Run(cfg config.Config, st *store.Store, c *gh.Client, j Job) error {
	key := j.Key()
	if cfg.NotifyTimeout.Duration > 0 {
		notifyTimeout = cfg.NotifyTimeout.Duration
	}
	var prev *store.AgentReview
	if j.RunID != 0 {
		if prev, _ = st.Run(j.RunID); prev != nil {
			j.Agent, j.Profile = prev.Agent, prev.Profile
		}
	}
	if j.Agent == "" {
		j.Agent = cfg.Agent.Main()
	}
	if j.RunID == 0 {
		id, err := st.StartRun(key, j.Head, j.Agent, j.Profile, os.Getpid())
		if err != nil {
			return fmt.Errorf("run: %w", err)
		}
		j.RunID = id
	}
	id := j.RunID
	_ = st.SetReviewPID(id, os.Getpid())
	be, err := For(cfg, j.Agent)
	if err != nil {
		_ = st.FailReview(id, err.Error())
		return err
	}
	fail := func(err error) error {
		if a := be.Auth(); a.Known && !a.LoggedIn {
			err = fmt.Errorf("%s isn't logged in; press %sl in prowl to log in", be.Title(), cfg.LeaderLabel())
		}
		if j.Refine != "" && prev != nil {
			_ = st.EndRevise(id)
			_ = st.AddReviewLog(key, store.Reviewer(be.Name(), j.Profile), "note", "the revise failed, so the review is as it was: "+err.Error())
			notify(be.Title()+" revise failed", key+": "+err.Error())
			return err
		}
		_ = st.FailReview(id, err.Error())
		notify(be.Title()+" review failed", key+": "+err.Error())
		return err
	}

	wt := j.Dir
	if wt == "" {
		_ = st.SetReviewActivity(id, "preparing worktree")
		if wt, err = prepareWorktree(cfg, j); err != nil {
			return fail(err)
		}
	}
	turn := func(input, session, phase string) (*result, string, error) {
		_ = st.SetReviewActivity(id, phase)
		return be.Review(Turn{
			Dir: wt, Prompt: input, Session: session, Model: cfg.Agent.Model(be.Name()),
			Schema:      Schema(cfg.Agent.Severities()),
			ContextDirs: existingDirs(cfg.ExpandedContextDirs()),
			Commands:    localCommands(cfg, be, j),
			Activity:    func(s string) { _ = st.SetReviewActivity(id, phase+": "+s) },
			SetModel:    func(m string) { _ = st.SetReviewModel(id, m) },
		})
	}

	var waiting map[string]store.ThreadReply
	fresh := func() string {
		runs, _ := st.Runs(key)
		var hist history
		var changed []string
		if j.Dir != "" {
			hist = localHistory(wt, j, runs, id)
			changed = localChanges(wt, hist.base)
		} else {
			_ = st.SetReviewActivity(id, "reading review history")
			hist = loadHistory(c, j)
			waiting = hist.waiting
			if hist.lastOid == "" && hist.reader != "" && hist.reader == j.Author {
				hist.lastOid, hist.lastAt, hist.byAgent = lastRun(runs, store.Reviewer(j.Agent, j.Profile), id)
			}
			if hist.lastOid != "" {
				_ = run(wt, "git", "fetch", "--quiet", "origin", hist.lastOid)
				if run(wt, "git", "cat-file", "-e", hist.lastOid+"^{commit}") != nil {
					hist.lastOidMissing = true
				}
			}
			out, _ := exec.Command("git", "-C", wt, "diff", "--name-only", "origin/"+j.Base+"...HEAD").Output()
			changed = strings.Fields(string(out))
		}
		_ = st.SetReviewActivity(id, "finding repo guidelines")
		var examples string
		if n := cfg.Agent.EditExamples; n > 0 {
			posts, _ := st.Posts(time.Time{})
			examples = editExamples(posts, n)
		}
		return prompt(j, cfg, be, findGuidelines(wt, changed), hist, dismissedSection(runs, id), examples)
	}

	if j.Refine != "" {
		if prev == nil || (be.Resumes() && prev.SessionID == "") {
			return fail(fmt.Errorf("no previous review to revise"))
		}
		if !j.Sync {
			_ = st.AddReviewLog(key, store.Reviewer(be.Name(), j.Profile), "you", j.Refine)
		}
		instruction := "Revise your review per this instruction from the reviewer, then return the complete updated review " +
			"(all findings you still stand behind, not just the changes). Put one or two sentences to the reviewer " +
			"about what you changed and why in `note`:\n\n" + j.Refine
		if j.Sync {
			instruction = j.Refine
			_ = st.AddReviewLog(key, store.Reviewer(be.Name(), j.Profile), "note", "synced the review from your chat")
		}
		session := prev.SessionID
		if !be.Resumes() {
			instruction = fresh() + previousReview(prev) + instruction
			session = ""
		}
		res, sid, err := turn(instruction, session, "revising")
		if err != nil {
			return fail(err)
		}
		if res.Note != "" {
			_ = st.AddReviewLog(key, store.Reviewer(be.Name(), j.Profile), "agent", res.Note)
		}
		keepState(res.Findings, prev.Findings)
		if waiting == nil && j.Dir == "" {
			waiting = loadHistory(c, j).waiting
		}
		return finish(st, be, id, key, sid, res, prev.Guidance, 0, cfg.Agent.Severities(), threadReplies(res, waiting, prev.Replies))
	}

	_ = st.ClearReviewLog(key, store.Reviewer(be.Name(), j.Profile))
	input := fresh()
	first, sid, err := turn(input, "", "reviewing")
	if err != nil {
		return fail(err)
	}

	verify, session := verifyPrompt, sid
	if !be.Resumes() {
		verify, session = input+draftReview(first)+verifyPrompt, ""
	}
	final, sid2, err := turn(verify, session, "verifying findings")
	if err != nil || final == nil {
		final, sid2 = first, sid
	}
	if final.Note != "" {
		_ = st.AddReviewLog(key, store.Reviewer(be.Name(), j.Profile), "agent", final.Note)
	}
	return finish(st, be, id, key, sid2, final, "", len(first.Findings), cfg.Agent.Severities(), threadReplies(final, waiting, nil))
}

// clean takes control characters out of everything the agent wrote: it
// reads PR text, and could copy an escape sequence from it.
func (r *result) clean() {
	r.Summary, r.Note, r.PRReason = gh.Clean(r.Summary), gh.Clean(r.Note), gh.Clean(r.PRReason)
	for i := range r.Findings {
		f := &r.Findings[i]
		f.Title, f.Body, f.Path, f.Rule = gh.Clean(f.Title), gh.Clean(f.Body), gh.Clean(f.Path), gh.Clean(f.Rule)
	}
	for i := range r.Points {
		p := &r.Points[i]
		p.Who, p.Path, p.Point, p.Why = gh.Clean(p.Who), gh.Clean(p.Path), gh.Clean(p.Point), gh.Clean(p.Why)
	}
}

// threadReplies keeps the agent's drafts for threads that are waiting on the
// reader, with where each thread is and what it answers. A draft for any
// other thread is dropped. A reply already sent stays marked sent.
func threadReplies(res *result, waiting map[string]store.ThreadReply, prev []store.ThreadReply) []store.ThreadReply {
	var out []store.ThreadReply
	for _, d := range res.ThreadReplies {
		r, ok := waiting[d.ThreadID]
		if body := strings.TrimSpace(d.Body); ok && body != "" {
			r.Body = body
			r.Posted = slices.ContainsFunc(prev, func(p store.ThreadReply) bool { return p.ThreadID == r.ThreadID && p.Posted })
			out = append(out, r)
		}
	}
	return out
}

// keepState carries the reviewer's choices (excluded, posted) over to the
// same findings in a revised review.
func keepState(next, prev []store.Finding) {
	for i := range next {
		for _, p := range prev {
			if p.Path == next[i].Path && p.Title == next[i].Title {
				next[i].Excluded, next[i].Posted = p.Excluded, p.Posted
			}
		}
	}
}

// dismissedSection lists findings the reviewer excluded from earlier reviews
// of this PR (by any agent), so the agent doesn't raise them again.
func dismissedSection(runs []*store.AgentReview, current int64) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, r := range runs {
		if r.ID == current {
			continue
		}
		for _, f := range r.Findings {
			k := f.Path + "|" + f.Title
			if !f.Excluded || f.Posted || seen[k] {
				continue
			}
			seen[k] = true
			where := "the PR"
			if f.Path != "" {
				where = fmt.Sprintf("%s:%d", f.Path, f.Line)
			}
			fmt.Fprintf(&b, "- %s (%s): %s\n", f.Title, where, oneLine(f.Body, 160))
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Findings the reviewer already dismissed\n\nEarlier reviews of this PR raised these, and the reviewer chose not to post them.\n" +
		"Do not raise them again unless the code changed in a way that makes them matter now.\n\n" + b.String() + "\n"
}

// previousReview shows an agent without sessions the review it is revising.
func previousReview(prev *store.AgentReview) string {
	b, _ := json.MarshalIndent(map[string]any{"summary": prev.Summary, "findings": prev.Findings, "points": prev.Points, "thread_replies": prev.Replies, "pr_action": prev.Action, "pr_action_reason": prev.ActionReason}, "", "  ")
	return "\n## Your previous review\n\n```json\n" + string(b) + "\n```\n\n"
}

// draftReview shows an agent without sessions the draft it is verifying.
func draftReview(r *result) string {
	b, _ := json.MarshalIndent(r, "", "  ")
	return "\n## Your draft review\n\nYou already reviewed this PR and wrote the draft below.\n\n```json\n" + string(b) + "\n```\n\n"
}

func existingDirs(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

const verifyPrompt = `Now verify your review before the reader sees it. For each finding:
1. Re-open the cited code at HEAD and confirm the claim is true. Run the command or read the file again; do not rely on memory.
2. Check it is not already raised, answered or resolved in the review history. If a reviewer raised it and it is still open, keep it only if you add something new.
3. Check it is inside this change, or, on a re-review, inside what changed since the reader's last review, unless it is must-fix.
Drop anything you cannot confirm. Lower severity that is overstated, and drop findings the severity rules leave out.
Check every point's verdict against the code at HEAD, and that a suggestion to close or draft the PR holds up; set it to none when it doesn't.
Check every thread reply against its thread and the code: it is accurate and answers what was said. Fix a reply that is off rather than dropping it; empty its body only if sending it would be wrong.
Then hold the summary, every body and every reply to the review instructions and the required skills, if any.
Return the complete final review in the same format. Do not mention this verification step in the summary.`

// finish stores the review. drafted is the finding count before verification
// (0 when there was no verification pass).
func finish(st *store.Store, be Backend, id int64, key, sessionID string, res *result, prevGuidance string, drafted int, severities []string, replies []store.ThreadReply) error {
	res.clean()
	for i := range replies {
		replies[i].Body = gh.Clean(replies[i].Body)
	}
	res.Findings = allowed(res.Findings, severities)
	summary := res.Summary
	if res.Verdict != "" {
		summary = "**Verdict: " + strings.ReplaceAll(res.Verdict, "_", " ") + "**\n\n" + summary
	}
	g := map[string]any{"guidelines": res.GuidelinesRead, "skills": res.SkillsUsed}
	if drafted > 0 || len(res.Findings) > 0 {
		g["verified"] = []int{drafted, len(res.Findings)}
	}
	if prevGuidance != "" && len(res.GuidelinesRead) == 0 {
		var prev map[string]any
		if json.Unmarshal([]byte(prevGuidance), &prev) == nil {
			g = prev
		}
	}
	guidance, _ := json.Marshal(g)
	if err := st.FinishReview(id, sessionID, summary, string(guidance), res.Findings); err != nil {
		return fmt.Errorf("finish: %w", err)
	}
	if err := st.SaveReplies(id, replies); err != nil {
		return fmt.Errorf("finish: %w", err)
	}
	if err := st.SavePoints(id, res.Points); err != nil {
		return fmt.Errorf("finish: %w", err)
	}
	action := res.PRAction
	if action != "close" && action != "draft" {
		action, res.PRReason = "", ""
	}
	if err := st.SaveAction(id, action, res.PRReason); err != nil {
		return fmt.Errorf("finish: %w", err)
	}
	notify(be.Title()+" review ready", readyText(key, &store.AgentReview{Findings: res.Findings, Replies: replies, Points: res.Points, Action: action}))
	return nil
}

// readyText says what a finished review holds, for its notification:
// "api#7: 3 findings, 1 must-fix · 2 still open · 2 replies to send".
func readyText(key string, r *store.AgentReview) string {
	_, pr, _ := strings.Cut(strings.TrimPrefix(key, "local:"), "/")
	text := "no findings"
	switch n := len(r.Findings); n {
	case 0:
	case 1:
		text = "1 finding"
	default:
		text = fmt.Sprintf("%d findings", n)
	}
	if n := r.MustFix(); n > 0 {
		text += fmt.Sprintf(", %d must-fix", n)
	}
	if n := r.StillOpen(); n > 0 {
		text += fmt.Sprintf(" · %d still open", n)
	}
	switch r.Action {
	case "close":
		text += " · suggests closing"
	case "draft":
		text += " · suggests a draft"
	}
	switch n := r.Unsent(); n {
	case 0:
	case 1:
		text += " · 1 reply to send"
	default:
		text += fmt.Sprintf(" · %d replies to send", n)
	}
	return pr + ": " + text
}

var notifyTimeout = 8 * time.Second

func notify(title, body string) { platform.Notify(title, body, notifyTimeout) }

// localCommands are the commands a review of your own checkout may run.
// Only Claude Code can be held to a list, and a PR review never runs any.
func localCommands(cfg config.Config, be Backend, j Job) []string {
	if j.Dir == "" || be.Name() != "claude" {
		return nil
	}
	return cfg.Agent.LocalCommands
}

// prepareWorktree ensures a detached worktree at the PR head and returns its
// path. It uses your clone under repo_roots when its origin is the PR's repo,
// and otherwise its own clone under the data dir.
func prepareWorktree(cfg config.Config, j Job) (string, error) {
	clone := clonePath(cfg, j.Repo)
	if _, err := os.Stat(clone); err != nil {
		if err := cloneRepo(j.Repo, clone); err != nil {
			return "", fmt.Errorf("prepare worktree: %w", err)
		}
	}
	ref := fmt.Sprintf("refs/prowl/pr-%d-%s", j.Number, strings.ReplaceAll(store.Reviewer(j.Agent, j.Profile), ":", "-"))
	fetch := func() error {
		return run(clone, "git", "fetch", "--quiet", "origin",
			fmt.Sprintf("+pull/%d/head:%s", j.Number, ref),
			fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", j.Base, j.Base))
	}
	err := fetch()
	for try := 0; err != nil && try < 3; try++ {
		time.Sleep(2 * time.Second)
		err = fetch()
	}
	if err != nil {
		return "", fmt.Errorf("fetch PR %d: %w", j.Number, err)
	}
	wt := WorktreePath(j.Repo, j.Number, store.Reviewer(j.Agent, j.Profile))
	if _, err := os.Stat(wt); err == nil {
		if err := run(wt, "git", "checkout", "--quiet", "--detach", "--force", ref); err == nil {
			return wt, nil
		}
		_ = run(clone, "git", "worktree", "remove", "--force", wt)
		_ = os.RemoveAll(wt)
	}
	_ = run(clone, "git", "worktree", "prune")
	if err := run(clone, "git", "worktree", "add", "--quiet", "--detach", wt, ref); err != nil {
		return "", fmt.Errorf("prepare worktree: %w", err)
	}
	return wt, nil
}

// clonePath is the clone a repo's reviews use: yours under repo_roots when
// its origin is the repo, else prowl's own under the data dir, which may not
// exist yet.
func clonePath(cfg config.Config, repo string) string {
	owner, name, _ := strings.Cut(repo, "/")
	for _, root := range cfg.ExpandedRepoRoots() {
		p := filepath.Join(root, name)
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil && strings.EqualFold(originRepo(p), repo) {
			return p
		}
	}
	return filepath.Join(config.DataDir(), "clones", owner, name)
}

func originRepo(dir string) string {
	out, _ := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url").Output()
	return gh.RemoteRepo(string(out))
}

// cloneRepo makes a partial clone of repo in dir: with gh, which uses your
// GitHub login, or with plain git and your git credentials when gh isn't
// installed. It clones beside dir and moves the clone into place, so
// reviewers starting at once never share a half-made clone: one that finds
// dir already there uses it.
func cloneRepo(repo, dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.cloning-%d", dir, os.Getpid())
	defer os.RemoveAll(tmp)
	var err error
	if _, lerr := exec.LookPath("gh"); lerr == nil {
		err = run("", "gh", "repo", "clone", repo, tmp, "--", "--filter=blob:none")
	} else {
		err = run("", "git", "clone", "--filter=blob:none", "https://github.com/"+repo+".git", tmp)
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, serr := os.Stat(filepath.Join(dir, ".git")); serr == nil {
			return nil
		}
		return err
	}
	return nil
}

// RemoveReview removes a PR's review worktrees and the refs its reviews
// fetched (best effort), once the PR is closed.
func RemoveReview(cfg config.Config, repo string, number int) {
	removeWorktrees(repo, number)
	clone := clonePath(cfg, repo)
	for _, ref := range prowlRefs(clone) {
		if refPR(ref) == number {
			_ = run(clone, "git", "update-ref", "-d", ref)
		}
	}
}

// PruneOrphans removes the review worktrees and refs/prowl/* refs of PRs that
// reviewed says prowl has no review of: left from before closed PRs lost
// their refs, or by a discarded review. reviewed is asked as it goes, so a
// review starting meanwhile keeps its own.
func PruneOrphans(cfg config.Config, reviewed func(repo string, number int) bool) {
	root := filepath.Join(config.DataDir(), "worktrees")
	owners, _ := os.ReadDir(root)
	for _, o := range owners {
		names, _ := os.ReadDir(filepath.Join(root, o.Name()))
		for _, n := range names {
			prs, _ := os.ReadDir(filepath.Join(root, o.Name(), n.Name()))
			for _, pr := range prs {
				if num, err := strconv.Atoi(pr.Name()); err == nil && !reviewed(o.Name()+"/"+n.Name(), num) {
					removeWorktrees(o.Name()+"/"+n.Name(), num)
				}
			}
		}
	}
	for _, dir := range checkoutDirs(cfg.ExpandedRepoRoots()) {
		repo := originRepo(dir)
		for _, ref := range prowlRefs(dir) {
			if !reviewed(repo, refPR(ref)) {
				_ = run(dir, "git", "update-ref", "-d", ref)
			}
		}
		_ = run(dir, "git", "worktree", "prune")
	}
}

// removeWorktrees removes every agent's worktree for a PR (best effort).
func removeWorktrees(repo string, number int) {
	dir := prWorktrees(repo, number)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		wt := filepath.Join(dir, e.Name())
		_ = run(wt, "git", "worktree", "remove", "--force", wt)
	}
	_ = os.RemoveAll(dir)
}

// Checkouts are the git checkouts directly under roots that hold prowl's
// review worktrees or the refs/prowl/* refs reviews fetch into them.
func Checkouts(roots []string) []string {
	var out []string
	for _, dir := range checkoutDirs(roots) {
		if strings.HasPrefix(dir, config.DataDir()) {
			continue
		}
		if wts, refs := prowlState(dir); len(wts)+len(refs) > 0 {
			out = append(out, dir)
		}
	}
	return out
}

// checkoutDirs are the repos reviews may fetch into: the ones directly under
// roots and prowl's own clones. A linked worktree is left out: it shares its
// repo's refs.
func checkoutDirs(roots []string) []string {
	var out []string
	for _, root := range append(roots, filepath.Join(config.DataDir(), "clones", "*")) {
		dirs, _ := filepath.Glob(filepath.Join(root, "*"))
		for _, dir := range dirs {
			if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
				out = append(out, dir)
			}
		}
	}
	return out
}

// CleanCheckout removes prowl's review worktrees and refs from a checkout.
func CleanCheckout(dir string) error {
	wts, refs := prowlState(dir)
	for _, wt := range wts {
		_ = run(dir, "git", "worktree", "remove", "--force", wt)
	}
	if err := run(dir, "git", "worktree", "prune"); err != nil {
		return err
	}
	for _, ref := range refs {
		if err := run(dir, "git", "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return nil
}

// prowlState is the worktrees prowl added to a checkout, which live under its
// data dir, and the refs/prowl/* refs it fetched.
func prowlState(dir string) (worktrees, refs []string) {
	root := filepath.Join(config.DataDir(), "worktrees")
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	out, _ := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	for l := range strings.Lines(string(out)) {
		if p, ok := strings.CutPrefix(strings.TrimSpace(l), "worktree "); ok && strings.HasPrefix(p, root+string(filepath.Separator)) {
			worktrees = append(worktrees, p)
		}
	}
	return worktrees, prowlRefs(dir)
}

// prowlRefs are the refs/prowl/* refs reviews fetched into a checkout.
func prowlRefs(dir string) []string {
	out, _ := exec.Command("git", "-C", dir, "for-each-ref", "--format=%(refname)", "refs/prowl/").Output()
	return strings.Fields(string(out))
}

// refPR is the PR a prowl ref is for: refs/prowl/pr-12-claude, or
// refs/prowl/pr-12 from older prowl. It is 0 for any other name.
func refPR(ref string) int {
	rest, _ := strings.CutPrefix(ref, "refs/prowl/pr-")
	digits, _, _ := strings.Cut(rest, "-")
	n, _ := strconv.Atoi(digits)
	return n
}

func run(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ReadJob reads the job a detached `prowl agent-run` was started with, and
// deletes its file.
func ReadJob(path string) (Job, error) {
	var j Job
	b, err := os.ReadFile(path)
	if err != nil {
		return j, fmt.Errorf("read job: %w", err)
	}
	_ = os.Remove(path)
	if err := json.Unmarshal(b, &j); err != nil {
		return j, fmt.Errorf("read job: %w", err)
	}
	return j, nil
}
