// Package store persists prowl's local state in SQLite: last-seen PR states
// (for notifications and response-time tracking), snoozes, and agent reviews.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tmstorm/prowl/internal/platform"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS pr_state (
  key TEXT PRIMARY KEY,
  bucket INTEGER NOT NULL,
  reason TEXT NOT NULL,
  head_oid TEXT NOT NULL,
  title TEXT NOT NULL,
  url TEXT NOT NULL,
  seen_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS snooze (
  key TEXT PRIMARY KEY,
  until INTEGER NOT NULL,
  head_oid TEXT NOT NULL   -- auto-unsnooze when the head changes
);
CREATE TABLE IF NOT EXISTS viewed (
  key TEXT PRIMARY KEY,
  at INTEGER NOT NULL      -- last time the PR was shown in the TUI
);
CREATE TABLE IF NOT EXISTS review_notes (
  key TEXT NOT NULL,       -- the PR
  reviewer TEXT NOT NULL,  -- agent, or agent:profile
  at INTEGER NOT NULL,
  who TEXT NOT NULL,       -- you | agent | note
  text TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS summaries (
  key TEXT PRIMARY KEY,    -- the PR
  head_oid TEXT NOT NULL,  -- the commit it was written for
  text TEXT NOT NULL,      -- markdown
  model TEXT NOT NULL,
  at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS inbox_cache (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  at INTEGER NOT NULL,
  data TEXT NOT NULL       -- the last fetched inbox (JSON), shown at startup
);
CREATE TABLE IF NOT EXISTS posts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT NOT NULL,       -- the PR
  url TEXT NOT NULL,
  at INTEGER NOT NULL,
  draft TEXT NOT NULL,     -- the review prowl put in your editor (JSON)
  posted TEXT NOT NULL     -- the review you posted (JSON)
);
CREATE TABLE IF NOT EXISTS review_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT NOT NULL,       -- the PR
  agent TEXT NOT NULL,     -- claude | codex | command
  profile TEXT NOT NULL DEFAULT '', -- review profile, e.g. adversarial
  status TEXT NOT NULL,    -- running | done | failed | posted
  head_oid TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  pid INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL DEFAULT 0,
  activity TEXT NOT NULL DEFAULT '',
  activity_at INTEGER NOT NULL DEFAULT 0, -- when activity last changed
  summary TEXT NOT NULL DEFAULT '',
  findings TEXT NOT NULL DEFAULT '[]',
  replies TEXT NOT NULL DEFAULT '[]', -- drafted replies to review threads
  points TEXT NOT NULL DEFAULT '[]',  -- points others raised, checked
  action TEXT NOT NULL DEFAULT '',    -- the agent suggests: close | draft | '' (neither)
  origin TEXT NOT NULL DEFAULT '',    -- 'local': ran in your checkout before the PR existed
  action_reason TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  posted_url TEXT NOT NULL DEFAULT '',
  guidance TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS review_runs_key ON review_runs(key, agent, id);
CREATE TABLE IF NOT EXISTS tips (
  name TEXT PRIMARY KEY    -- a one-time tip prowl has shown
);
CREATE TABLE IF NOT EXISTS watcher (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  pid INTEGER NOT NULL     -- the running prowl watch, which sends the notifications
);
CREATE TABLE IF NOT EXISTS locals (
  key TEXT PRIMARY KEY,    -- local:owner/name@branch
  repo TEXT NOT NULL,      -- owner/name
  branch TEXT NOT NULL,
  dir TEXT NOT NULL,       -- the checkout
  base TEXT NOT NULL,      -- the default branch it is compared with
  at INTEGER NOT NULL      -- last reviewed
);
`

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "prowl.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ---- PR state snapshots

type PRState struct {
	Key, Reason, HeadOid, Title, URL string
	Bucket                           int
}

func (s *Store) AllStates() (map[string]PRState, error) {
	rows, err := s.db.Query(`SELECT key, bucket, reason, head_oid, title, url FROM pr_state`)
	if err != nil {
		return nil, fmt.Errorf("all states: %w", err)
	}
	defer rows.Close()
	out := map[string]PRState{}
	for rows.Next() {
		var p PRState
		if err := rows.Scan(&p.Key, &p.Bucket, &p.Reason, &p.HeadOid, &p.Title, &p.URL); err != nil {
			return nil, fmt.Errorf("all states: %w", err)
		}
		out[p.Key] = p
	}
	return out, rows.Err()
}

// ReplaceStates stores the current snapshot of the inbox.
func (s *Store) ReplaceStates(states []PRState) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("replace states: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.Exec(`DELETE FROM pr_state`); err != nil {
		return fmt.Errorf("replace states: %w", err)
	}
	for _, st := range states {
		if _, err := tx.Exec(`INSERT INTO pr_state VALUES (?,?,?,?,?,?,?)`,
			st.Key, st.Bucket, st.Reason, st.HeadOid, st.Title, st.URL, now); err != nil {
			return fmt.Errorf("replace states: %w", err)
		}
	}
	return tx.Commit()
}

// ---- snooze

func (s *Store) Snooze(key, headOid string, d time.Duration) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO snooze VALUES (?,?,?)`, key, time.Now().Add(d).Unix(), headOid)
	return wrap("snooze", err)
}

func (s *Store) Unsnooze(key string) error {
	_, err := s.db.Exec(`DELETE FROM snooze WHERE key = ?`, key)
	return wrap("unsnooze", err)
}

// Snoozed returns key -> until for snoozes still in effect given current head oids.
func (s *Store) Snoozed(heads map[string]string) (map[string]time.Time, error) {
	rows, err := s.db.Query(`SELECT key, until, head_oid FROM snooze`)
	if err != nil {
		return nil, fmt.Errorf("snoozed: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	now := time.Now()
	for rows.Next() {
		var key, head string
		var until int64
		if err := rows.Scan(&key, &until, &head); err != nil {
			return nil, fmt.Errorf("snoozed: %w", err)
		}
		t := time.Unix(until, 0)
		if t.After(now) && (heads[key] == "" || heads[key] == head) {
			out[key] = t
		}
	}
	return out, rows.Err()
}

// ---- viewed (what has been read)

// MarkViewed records that the PR was looked at now and returns the previous
// time (zero if never), so callers can tell new comments from read ones.
func (s *Store) MarkViewed(key string) (time.Time, error) {
	var prev int64
	_ = s.db.QueryRow(`SELECT at FROM viewed WHERE key = ?`, key).Scan(&prev)
	_, err := s.db.Exec(`INSERT OR REPLACE INTO viewed VALUES (?, ?)`, key, time.Now().Unix())
	if prev == 0 {
		return time.Time{}, err
	}
	return time.Unix(prev, 0), err
}

// ---- agent reviews

type Finding struct {
	Severity string `json:"severity"`       // must-fix | should-fix | nit
	Rule     string `json:"rule,omitempty"` // guideline the finding comes from
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Excluded bool   `json:"excluded,omitempty"`
	Posted   bool   `json:"posted,omitempty"` // went out in a GitHub review
}

// Point is something a reviewer, a bot or a check raised on the PR, as an
// agent checked it against the code. Points are for you only, never posted.
type Point struct {
	Who     string `json:"who"` // login, or the check's name
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Point   string `json:"point"`
	Verdict string `json:"verdict"` // addressed | open | wrong | unclear
	Why     string `json:"why"`
}

// ThreadReply is a reply an agent drafted to a review thread you are in,
// for you to edit and send.
type ThreadReply struct {
	ThreadID string `json:"thread_id"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	To       string `json:"to"`   // who wrote the comment it answers
	Last     string `json:"last"` // that comment
	Body     string `json:"body"`
	Excluded bool   `json:"excluded,omitempty"` // you chose not to send it
	Posted   bool   `json:"posted,omitempty"`
}

// AgentReview is one review run: an agent reviewing a PR at one commit. A
// revise or sync updates the run; a new review starts a new one, and the
// earlier runs are the agent's history for that PR.
type AgentReview struct {
	ID                              int64
	Key, Status, HeadOid, SessionID string
	PID                             int
	StartedAt, FinishedAt           time.Time
	Activity, Summary, Error        string
	ActivityAt                      time.Time // when Activity last changed
	Guidance                        string    // what the review followed (guidelines, skills)
	Model                           string    // model ID the session ran on
	Agent                           string    // backend that ran it: claude, codex, command
	Profile                         string    // review profile it ran with, "" for a plain review
	PostedURL                       string
	Findings                        []Finding
	Replies                         []ThreadReply
	Points                          []Point
	Action, ActionReason            string // the agent suggests closing the PR ("close") or a draft ("draft"), and why
	// Origin is "local" for a run made in your checkout before the PR
	// existed, then moved to the PR; its session lives in that checkout.
	Origin string
}

// MustFix counts the review's must-fix findings.
func (r *AgentReview) MustFix() int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == "must-fix" {
			n++
		}
	}
	return n
}

// StillOpen counts the points raised that still hold.
func (r *AgentReview) StillOpen() int {
	n := 0
	for _, p := range r.Points {
		if p.Verdict == "open" {
			n++
		}
	}
	return n
}

// Unsent counts the drafted thread replies still to send: not sent, and
// not left out.
func (r *AgentReview) Unsent() int {
	n := 0
	for _, rp := range r.Replies {
		if !rp.Posted && !rp.Excluded {
			n++
		}
	}
	return n
}

// Reviewer names who reviewed: the agent, or agent:profile.
func (r *AgentReview) Reviewer() string { return Reviewer(r.Agent, r.Profile) }

// Reviewer is the key for an agent running with a profile ("" for none).
func Reviewer(agent, profile string) string {
	if profile == "" {
		return agent
	}
	return agent + ":" + profile
}

// StartRun records a new review run and returns its ID.
func (s *Store) StartRun(key, headOid, agent, profile string, pid int) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO review_runs (key, agent, profile, status, head_oid, pid, started_at) VALUES (?, ?, ?, 'running', ?, ?, ?)`,
		key, agent, profile, headOid, pid, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("start run: %w", err)
	}
	return res.LastInsertId()
}

// RestartRun marks a run running again, for a revise or sync in its session.
func (s *Store) RestartRun(id int64, pid int) error {
	_, err := s.db.Exec(`UPDATE review_runs SET status='running', pid=?, started_at=?, finished_at=0, error='', activity='' WHERE id=?`,
		pid, time.Now().Unix(), id)
	return wrap("restart run", err)
}

func (s *Store) SetReviewPID(id int64, pid int) error {
	_, err := s.db.Exec(`UPDATE review_runs SET pid=? WHERE id=?`, pid, id)
	return wrap("set review pid", err)
}

func (s *Store) SetReviewModel(id int64, model string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET model=? WHERE id=?`, model, id)
	return wrap("set review model", err)
}

func (s *Store) SetReviewActivity(id int64, activity string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET activity=?, activity_at=? WHERE id=?`, activity, time.Now().Unix(), id)
	return wrap("set review activity", err)
}

func (s *Store) FinishReview(id int64, sessionID, summary, guidance string, findings []Finding) error {
	b, _ := json.Marshal(findings)
	_, err := s.db.Exec(`UPDATE review_runs SET status='done', session_id=?, summary=?, guidance=?, findings=?, finished_at=?, activity='' WHERE id=?`,
		sessionID, summary, guidance, string(b), time.Now().Unix(), id)
	return wrap("finish review", err)
}

// EndRevise puts a run whose revise failed back as it was: done, or posted
// when it went out. Its review is unchanged.
func (s *Store) EndRevise(id int64) error {
	_, err := s.db.Exec(`UPDATE review_runs SET status = CASE WHEN posted_url != '' THEN 'posted' ELSE 'done' END,
  finished_at=?, activity='', error='' WHERE id=?`, time.Now().Unix(), id)
	return wrap("end revise", err)
}

func (s *Store) FailReview(id int64, msg string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET status='failed', error=?, finished_at=?, activity='' WHERE id=?`,
		msg, time.Now().Unix(), id)
	return wrap("fail review", err)
}

// SaveAction records what the agent suggests doing with the PR: "close",
// "draft" or "" for neither, and why.
func (s *Store) SaveAction(id int64, action, reason string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET action=?, action_reason=? WHERE id=?`, action, reason, id)
	return wrap("save action", err)
}

// SavePoints replaces the points a run checked.
func (s *Store) SavePoints(id int64, points []Point) error {
	b, _ := json.Marshal(points)
	_, err := s.db.Exec(`UPDATE review_runs SET points=? WHERE id=?`, string(b), id)
	return wrap("save points", err)
}

// SaveReplies replaces a run's drafted thread replies.
func (s *Store) SaveReplies(id int64, replies []ThreadReply) error {
	b, _ := json.Marshal(replies)
	_, err := s.db.Exec(`UPDATE review_runs SET replies=? WHERE id=?`, string(b), id)
	return wrap("save replies", err)
}

func (s *Store) SaveFindings(id int64, findings []Finding) error {
	b, _ := json.Marshal(findings)
	_, err := s.db.Exec(`UPDATE review_runs SET findings=? WHERE id=?`, string(b), id)
	return wrap("save findings", err)
}

func (s *Store) MarkPosted(id int64, url string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET status='posted', posted_url=? WHERE id=?`, url, id)
	return wrap("mark posted", err)
}

// DeleteRun discards one run; the agent's previous run becomes current.
func (s *Store) DeleteRun(id int64) error {
	_, err := s.db.Exec(`DELETE FROM review_runs WHERE id=?`, id)
	return wrap("delete run", err)
}

// ReviewedPRs are the PRs that have review runs or notes.
func (s *Store) ReviewedPRs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT key FROM review_runs UNION SELECT DISTINCT key FROM review_notes UNION SELECT key FROM summaries`)
	if err != nil {
		return nil, fmt.Errorf("reviewed PRs: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("reviewed PRs: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteReviews deletes a PR's review runs and notes, once it is merged or
// closed.
func (s *Store) DeleteReviews(key string) error {
	if _, err := s.db.Exec(`DELETE FROM review_runs WHERE key=?`, key); err != nil {
		return fmt.Errorf("delete reviews of %s: %w", key, err)
	}
	if _, err := s.db.Exec(`DELETE FROM review_notes WHERE key=?`, key); err != nil {
		return fmt.Errorf("delete notes of %s: %w", key, err)
	}
	if _, err := s.db.Exec(`DELETE FROM summaries WHERE key=?`, key); err != nil {
		return fmt.Errorf("delete summary of %s: %w", key, err)
	}
	return nil
}

// ---- conversation with the review agent (local only, never posted)

type LogEntry struct {
	At        time.Time
	Who, Text string
}

func (s *Store) AddReviewLog(key, reviewer, who, text string) error {
	_, err := s.db.Exec(`INSERT INTO review_notes (key, reviewer, at, who, text) VALUES (?,?,?,?,?)`, key, reviewer, time.Now().UnixNano(), who, text)
	return wrap("add review log", err)
}

func (s *Store) ClearReviewLog(key, reviewer string) error {
	_, err := s.db.Exec(`DELETE FROM review_notes WHERE key=? AND reviewer=?`, key, reviewer)
	return wrap("clear review log", err)
}

func (s *Store) ReviewLog(key, reviewer string) ([]LogEntry, error) {
	rows, err := s.db.Query(`SELECT at, who, text FROM review_notes WHERE key=? AND reviewer=? ORDER BY at`, key, reviewer)
	if err != nil {
		return nil, fmt.Errorf("review log: %w", err)
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		var at int64
		if err := rows.Scan(&at, &e.Who, &e.Text); err != nil {
			return nil, fmt.Errorf("review log: %w", err)
		}
		e.At = time.Unix(0, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Run is one review run, or nil.
func (s *Store) Run(id int64) (*AgentReview, error) {
	all, err := s.reviews(`WHERE id = ?`, id)
	if err != nil || len(all) == 0 {
		return nil, wrap("run", err)
	}
	return all[0], nil
}

// Runs are a PR's review runs from every agent, newest first.
func (s *Store) Runs(key string) ([]*AgentReview, error) {
	return s.reviews(`WHERE key = ? ORDER BY id DESC`, key)
}

// AllRuns are every PR's review runs, newest first.
func (s *Store) AllRuns() (map[string][]*AgentReview, error) {
	all, err := s.reviews(`ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("all runs: %w", err)
	}
	out := map[string][]*AgentReview{}
	for _, c := range all {
		out[c.Key] = append(out[c.Key], c)
	}
	return out, nil
}

// Latest is a reviewer's current run in runs (newest first), or nil.
func Latest(runs []*AgentReview, reviewer string) *AgentReview {
	for _, r := range runs {
		if r.Reviewer() == reviewer {
			return r
		}
	}
	return nil
}

// Current is each reviewer's latest run, newest first.
func Current(runs []*AgentReview) []*AgentReview {
	var out []*AgentReview
	seen := map[string]bool{}
	for _, r := range runs {
		if !seen[r.Reviewer()] {
			seen[r.Reviewer()] = true
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) reviews(where string, args ...any) ([]*AgentReview, error) {
	rows, err := s.db.Query(`SELECT id, key, status, head_oid, session_id, pid, started_at, finished_at,
  activity, activity_at, summary, findings, replies, points, action, action_reason, origin, error, posted_url, guidance, model, agent, profile FROM review_runs `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("reviews: %w", err)
	}
	defer rows.Close()
	var out []*AgentReview
	for rows.Next() {
		var c AgentReview
		var started, finished, activityAt int64
		var findings, replies, points string
		if err := rows.Scan(&c.ID, &c.Key, &c.Status, &c.HeadOid, &c.SessionID, &c.PID, &started, &finished,
			&c.Activity, &activityAt, &c.Summary, &findings, &replies, &points, &c.Action, &c.ActionReason, &c.Origin, &c.Error, &c.PostedURL, &c.Guidance, &c.Model, &c.Agent, &c.Profile); err != nil {
			return nil, fmt.Errorf("reviews: %w", err)
		}
		c.StartedAt = time.Unix(started, 0)
		if finished > 0 {
			c.FinishedAt = time.Unix(finished, 0)
		}
		if activityAt > 0 {
			c.ActivityAt = time.Unix(activityAt, 0)
		}
		_ = json.Unmarshal([]byte(findings), &c.Findings)
		_ = json.Unmarshal([]byte(replies), &c.Replies)
		_ = json.Unmarshal([]byte(points), &c.Points)

		if c.Status == "running" && c.PID > 0 && !platform.Alive(c.PID) {
			c.Status = "failed"
			c.Error = "review process exited unexpectedly"
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// wrap adds the operation to a database error, leaving nil alone.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}

// Comment is one inline comment of a review: where, and what it says.
type Comment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// Review is a review's text: the body and its inline comments.
type Review struct {
	Body     string    `json:"body"`
	Comments []Comment `json:"comments"`
}

// Post is a posted agent review: the draft prowl made, and what you posted
// after editing it.
type Post struct {
	Key, URL      string
	At            time.Time
	Draft, Posted Review
}

// AddPost records a posted agent review next to its draft.
func (s *Store) AddPost(key, url string, draft, posted Review) error {
	d, _ := json.Marshal(draft)
	p, _ := json.Marshal(posted)
	_, err := s.db.Exec(`INSERT INTO posts (key, url, at, draft, posted) VALUES (?,?,?,?,?)`, key, url, time.Now().Unix(), string(d), string(p))
	return wrap("add post", err)
}

// Posts are the posted agent reviews since a time, oldest first.
func (s *Store) Posts(since time.Time) ([]Post, error) {
	rows, err := s.db.Query(`SELECT key, url, at, draft, posted FROM posts WHERE at >= ? ORDER BY at`, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("posts: %w", err)
	}
	defer rows.Close()
	var out []Post
	for rows.Next() {
		var p Post
		var at int64
		var d, posted string
		if err := rows.Scan(&p.Key, &p.URL, &at, &d, &posted); err != nil {
			return nil, fmt.Errorf("posts: %w", err)
		}
		p.At = time.Unix(at, 0)
		_ = json.Unmarshal([]byte(d), &p.Draft)
		_ = json.Unmarshal([]byte(posted), &p.Posted)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveInbox keeps the last fetched inbox, so the next start can show it
// before its first sync finishes.
func (s *Store) SaveInbox(data []byte, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO inbox_cache (id, at, data) VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET at=excluded.at, data=excluded.data`, at.Unix(), string(data))
	return wrap("save inbox", err)
}

// LastInbox is the inbox SaveInbox kept, or nil.
func (s *Store) LastInbox() ([]byte, time.Time, error) {
	var at int64
	var data string
	err := s.db.QueryRow(`SELECT at, data FROM inbox_cache WHERE id = 1`).Scan(&at, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("last inbox: %w", err)
	}
	return []byte(data), time.Unix(at, 0), nil
}

// Summary is an agent's plain-language summary of a PR at one commit.
type Summary struct {
	Key, HeadOid, Text, Model string
	At                        time.Time
}

// SaveSummary keeps a PR's summary, replacing any earlier one.
func (s *Store) SaveSummary(sum Summary) error {
	_, err := s.db.Exec(`INSERT INTO summaries (key, head_oid, text, model, at) VALUES (?,?,?,?,?)
ON CONFLICT(key) DO UPDATE SET head_oid=excluded.head_oid, text=excluded.text, model=excluded.model, at=excluded.at`,
		sum.Key, sum.HeadOid, sum.Text, sum.Model, sum.At.Unix())
	return wrap("save summary", err)
}

// Summaries are every PR's summary, by PR.
func (s *Store) Summaries() (map[string]*Summary, error) {
	rows, err := s.db.Query(`SELECT key, head_oid, text, model, at FROM summaries`)
	if err != nil {
		return nil, fmt.Errorf("summaries: %w", err)
	}
	defer rows.Close()
	out := map[string]*Summary{}
	for rows.Next() {
		var sum Summary
		var at int64
		if err := rows.Scan(&sum.Key, &sum.HeadOid, &sum.Text, &sum.Model, &at); err != nil {
			return nil, fmt.Errorf("summaries: %w", err)
		}
		sum.At = time.Unix(at, 0)
		out[sum.Key] = &sum
	}
	return out, rows.Err()
}

// ---- local reviews

// Local is a branch in a checkout that prowl reviewed before it has a pull
// request.
type Local struct {
	Key, Repo, Branch, Dir, Base string
	At                           time.Time
}

// LocalKey names a local review: local:owner/name@branch.
func LocalKey(repo, branch string) string { return "local:" + repo + "@" + branch }

// SaveLocal records a local review, replacing one for the same branch.
func (s *Store) SaveLocal(l Local) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO locals (key, repo, branch, dir, base, at) VALUES (?,?,?,?,?,?)`,
		l.Key, l.Repo, l.Branch, l.Dir, l.Base, l.At.Unix())
	return wrap("save local", err)
}

// Locals are every local review, newest first.
func (s *Store) Locals() ([]Local, error) {
	rows, err := s.db.Query(`SELECT key, repo, branch, dir, base, at FROM locals ORDER BY at DESC`)
	if err != nil {
		return nil, fmt.Errorf("locals: %w", err)
	}
	defer rows.Close()
	var out []Local
	for rows.Next() {
		var l Local
		var at int64
		if err := rows.Scan(&l.Key, &l.Repo, &l.Branch, &l.Dir, &l.Base, &at); err != nil {
			return nil, fmt.Errorf("locals: %w", err)
		}
		l.At = time.Unix(at, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteLocal forgets a local review and its runs.
func (s *Store) DeleteLocal(key string) error {
	if _, err := s.db.Exec(`DELETE FROM locals WHERE key=?`, key); err != nil {
		return wrap("delete local", err)
	}
	return s.DeleteReviews(key)
}

// MoveLocal moves a local review to the PR opened for its branch: its runs
// (marked as from before the PR), notes and summary go to prKey, and the
// local review is forgotten. A summary the PR already has stays.
func (s *Store) MoveLocal(key, prKey string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return wrap("move local", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`UPDATE review_runs SET key=?, origin='local' WHERE key=?`,
		`UPDATE review_notes SET key=? WHERE key=?`,
		`UPDATE OR IGNORE summaries SET key=? WHERE key=?`,
	} {
		if _, err := tx.Exec(q, prKey, key); err != nil {
			return wrap("move local", err)
		}
	}
	for _, q := range []string{`DELETE FROM summaries WHERE key=?`, `DELETE FROM locals WHERE key=?`} {
		if _, err := tx.Exec(q, key); err != nil {
			return wrap("move local", err)
		}
	}
	return wrap("move local", tx.Commit())
}

// FirstTime reports whether this is the first time name was asked about,
// and remembers that it was: for tips shown once.
func (s *Store) FirstTime(name string) bool {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO tips (name) VALUES (?)`, name)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// SetWatcher records the process ID of the running `prowl watch`.
func (s *Store) SetWatcher(pid int) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO watcher (id, pid) VALUES (1, ?)`, pid)
	return wrap("set watcher", err)
}

// Watcher is the process ID `prowl watch` last recorded, or 0.
func (s *Store) Watcher() int {
	var pid int
	_ = s.db.QueryRow(`SELECT pid FROM watcher WHERE id = 1`).Scan(&pid)
	return pid
}
