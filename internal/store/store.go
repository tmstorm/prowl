// Package store persists prowl's local state in SQLite: the inbox as last
// notified, snoozes, agent reviews with their notes, summaries, Slack shares
// and local reviews.
package store

import (
	"context"
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
  head_oid TEXT NOT NULL
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
CREATE TABLE IF NOT EXISTS extension_runs (
  key TEXT NOT NULL,       -- the PR
  name TEXT NOT NULL,      -- the extension
  head_oid TEXT NOT NULL,
  status TEXT NOT NULL,    -- running | done | failed
  claim TEXT NOT NULL,     -- who holds it while running: pid and claim time
  holder TEXT NOT NULL,    -- the reviewer whose review runs it, e.g. codex
  pid INTEGER NOT NULL,
  deadline INTEGER NOT NULL, -- unix seconds; a holder past it has failed
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL DEFAULT 0,
  output TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (key, name, head_oid)
);
CREATE TABLE IF NOT EXISTS extension_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT NOT NULL,       -- the PR
  name TEXT NOT NULL,      -- the on-demand extension
  head_oid TEXT NOT NULL,
  status TEXT NOT NULL,    -- running | done | failed
  pid INTEGER NOT NULL DEFAULT 0,
  deadline INTEGER NOT NULL, -- unix seconds; a job running past it has failed
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL DEFAULT 0,
  output TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  added INTEGER NOT NULL DEFAULT 0 -- findings it added to the AI review
);
CREATE INDEX IF NOT EXISTS extension_jobs_key ON extension_jobs(key, name, id);
CREATE TABLE IF NOT EXISTS cancels ( -- what you cancelled while it ran, so its agent-run stops
  kind TEXT NOT NULL,      -- run | job
  id INTEGER NOT NULL,     -- the review run or extension job
  PRIMARY KEY (kind, id)
);
CREATE TABLE IF NOT EXISTS tips (
  name TEXT PRIMARY KEY    -- a one-time tip prowl has shown
);
CREATE TABLE IF NOT EXISTS releases (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  checked_at INTEGER NOT NULL,         -- when prowl last asked GitHub for its newest release
  tag TEXT NOT NULL,                   -- that release, e.g. v0.2.0
  notes TEXT NOT NULL,
  url TEXT NOT NULL,
  installed TEXT NOT NULL DEFAULT '',  -- the release prowl installed itself; it runs after a restart
  attempted TEXT NOT NULL DEFAULT ''   -- the last release prowl tried to install in the background
);
CREATE TABLE IF NOT EXISTS watcher (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  pid INTEGER NOT NULL,    -- the running prowl watch, which is favored for the background work
  at INTEGER NOT NULL DEFAULT 0 -- when it last said it runs; it says so every few seconds
);
CREATE TABLE IF NOT EXISTS locals (
  key TEXT PRIMARY KEY,    -- local:owner/name@branch
  repo TEXT NOT NULL,      -- owner/name
  branch TEXT NOT NULL,
  dir TEXT NOT NULL,       -- the checkout
  base TEXT NOT NULL,      -- the default branch it is compared with
  at INTEGER NOT NULL      -- last reviewed
);
CREATE TABLE IF NOT EXISTS slack_shares (
  channel TEXT NOT NULL,   -- Slack channel ID
  ts TEXT NOT NULL,        -- the message
  pr_key TEXT NOT NULL,
  pr_id TEXT NOT NULL,     -- the PR's node ID
  channel_name TEXT NOT NULL, -- '' for a DM
  thread_ts TEXT NOT NULL, -- '' at the top level
  user TEXT NOT NULL,      -- who posted it (Slack ID)
  user_name TEXT NOT NULL,
  at INTEGER NOT NULL,
  link TEXT NOT NULL DEFAULT '',        -- the message's permalink
  my_reaction TEXT NOT NULL DEFAULT '', -- the emoji prowl last set, on others' PRs
  PRIMARY KEY (channel, ts, pr_key)
);
CREATE TABLE IF NOT EXISTS pending_comments (
  key TEXT PRIMARY KEY,    -- the PR
  comments TEXT NOT NULL   -- your inline comments for your next review (JSON)
);
CREATE TABLE IF NOT EXISTS slack_signin (
  id INTEGER PRIMARY KEY CHECK (id = 1) -- a row: a Slack token is in the keyring
);
CREATE TABLE IF NOT EXISTS slack_search (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  at INTEGER NOT NULL      -- the last search for shares that succeeded
);
CREATE TABLE IF NOT EXISTS service (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  sum TEXT NOT NULL        -- the service definition prowl last wrote, as platform.ServiceSum
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
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return &Store{db: db}, nil
}

// migrations change the tables of a database an older prowl made, oldest
// first; its user_version counts the ones it has run. A new database is
// made from schema and starts after them all.
var migrations = []string{
	`ALTER TABLE pr_state DROP COLUMN title; ALTER TABLE pr_state DROP COLUMN url; ALTER TABLE pr_state DROP COLUMN seen_at`,
	`CREATE TABLE IF NOT EXISTS watcher (id INTEGER PRIMARY KEY CHECK (id = 1), pid INTEGER NOT NULL);
ALTER TABLE watcher ADD COLUMN at INTEGER NOT NULL DEFAULT 0`,
}

// migrate brings the database up to date. It holds the write lock
// throughout, so two prowls starting at once migrate it once.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	var version, tables int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&tables); err != nil {
		return err
	}
	if tables == 0 {
		version = len(migrations)
	}
	for i := version; i < len(migrations); i++ {
		if _, err := conn.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, max(version, len(migrations)))); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// ---- PR state snapshots

type PRState struct {
	Key, Reason, HeadOid string
	Bucket               int
}

func (s *Store) AllStates() (map[string]PRState, error) {
	rows, err := s.db.Query(`SELECT key, bucket, reason, head_oid FROM pr_state`)
	if err != nil {
		return nil, fmt.Errorf("all states: %w", err)
	}
	defer rows.Close()
	out := map[string]PRState{}
	for rows.Next() {
		var p PRState
		if err := rows.Scan(&p.Key, &p.Bucket, &p.Reason, &p.HeadOid); err != nil {
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
	if _, err := tx.Exec(`DELETE FROM pr_state`); err != nil {
		return fmt.Errorf("replace states: %w", err)
	}
	for _, st := range states {
		if _, err := tx.Exec(`INSERT INTO pr_state VALUES (?,?,?,?)`, st.Key, st.Bucket, st.Reason, st.HeadOid); err != nil {
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
	// ExtensionID is the extension finding this is, e.g. perf-1.
	ExtensionID string `json:"extension_id,omitempty"`
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

// ThreadReply is a reply an agent drafted to a review thread, for you to
// edit and send.
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

// Run is one review run: an agent reviewing a PR at one commit. A
// revise or sync updates the run; a new review starts a new one, and the
// earlier runs are the agent's history for that PR.
type Run struct {
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
	Findings                        []Finding
	Replies                         []ThreadReply
	Points                          []Point
	Action, ActionReason            string // the agent suggests closing the PR ("close") or a draft ("draft"), and why
	// Origin is "local" for a run made in your checkout before the PR
	// existed, then moved to the PR; its session lives in that checkout.
	Origin string
}

// MustFix counts the review's must-fix findings.
func (r *Run) MustFix() int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == "must-fix" {
			n++
		}
	}
	return n
}

// StillOpen counts the points raised that still hold.
func (r *Run) StillOpen() int {
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
func (r *Run) Unsent() int {
	n := 0
	for _, rp := range r.Replies {
		if !rp.Posted && !rp.Excluded {
			n++
		}
	}
	return n
}

// Reviewer names who reviewed: the agent, or agent:profile.
func (r *Run) Reviewer() string { return Reviewer(r.Agent, r.Profile) }

// Finished reports whether the review ran to its end, posted or not.
func (r *Run) Finished() bool { return r.Status == "done" || r.Status == "posted" }

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
	if _, err := s.db.Exec(`DELETE FROM cancels WHERE kind='run' AND id=?`, id); err != nil {
		return wrap("restart run", err)
	}
	_, err := s.db.Exec(`UPDATE review_runs SET status='running', pid=?, started_at=?, finished_at=0, error='', activity='' WHERE id=?`,
		pid, time.Now().Unix(), id)
	return wrap("restart run", err)
}

func (s *Store) SetRunPID(id int64, pid int) error {
	_, err := s.db.Exec(`UPDATE review_runs SET pid=? WHERE id=?`, pid, id)
	return wrap("set review pid", err)
}

func (s *Store) SetRunModel(id int64, model string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET model=? WHERE id=?`, model, id)
	return wrap("set review model", err)
}

func (s *Store) SetRunActivity(id int64, activity string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET activity=?, activity_at=? WHERE id=?`, activity, time.Now().Unix(), id)
	return wrap("set review activity", err)
}

func (s *Store) FinishRun(id int64, sessionID, summary, guidance string, findings []Finding) error {
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

func (s *Store) FailRun(id int64, msg string) error {
	_, err := s.db.Exec(`UPDATE review_runs SET status='failed', error=?, finished_at=?, activity='' WHERE id=?`,
		msg, time.Now().Unix(), id)
	return wrap("fail review", err)
}

// CancelRun asks a running run's agent-run to stop, which ends the run. It
// reports false when the run isn't running.
func (s *Store) CancelRun(id int64) (bool, error) {
	return s.cancel("run", `SELECT id FROM review_runs WHERE id=? AND status='running'`, id)
}

// RunCancelled reports whether you cancelled the run.
func (s *Store) RunCancelled(id int64) bool { return s.cancelled("run", id) }

func (s *Store) cancel(kind, running string, id int64) (bool, error) {
	res, err := s.db.Exec(`INSERT OR REPLACE INTO cancels (kind, id) SELECT ?, id FROM (`+running+`)`, kind, id)
	if err != nil {
		return false, wrap("cancel "+kind, err)
	}
	n, err := res.RowsAffected()
	return n > 0, wrap("cancel "+kind, err)
}

func (s *Store) cancelled(kind string, id int64) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM cancels WHERE kind=? AND id=?`, kind, id).Scan(&one) == nil
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

// ReviewedPRs are the PRs that have review runs, notes, a summary or
// on-demand extension jobs.
func (s *Store) ReviewedPRs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT key FROM review_runs UNION SELECT DISTINCT key FROM review_notes UNION SELECT key FROM summaries
		UNION SELECT DISTINCT key FROM extension_jobs UNION SELECT key FROM pending_comments`)
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

// DeleteReviews deletes a PR's review runs, notes and pending comments,
// once it is merged or closed.
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
	if _, err := s.db.Exec(`DELETE FROM extension_runs WHERE key=?`, key); err != nil {
		return fmt.Errorf("delete extension runs of %s: %w", key, err)
	}
	if _, err := s.db.Exec(`DELETE FROM extension_jobs WHERE key=?`, key); err != nil {
		return fmt.Errorf("delete extension jobs of %s: %w", key, err)
	}
	if _, err := s.db.Exec(`DELETE FROM pending_comments WHERE key=?`, key); err != nil {
		return fmt.Errorf("delete pending comments of %s: %w", key, err)
	}
	return nil
}

// ExtensionJob is an on-demand extension's run on a PR, started from a
// frontend and run by a detached prowl. Output is the extension's JSON, as
// much as it printed when it failed.
type ExtensionJob struct {
	ID                         int64
	Key, Name, HeadOid, Status string
	PID                        int
	Deadline                   time.Time
	StartedAt, FinishedAt      time.Time
	Output, Error              string
	Added                      int
}

// ErrExtensionRunning refuses to start an extension already running on a PR.
var ErrExtensionRunning = errors.New("already running on this PR")

// StartExtensionJob records that extension name starts on the PR, unless it
// is already running there: one job of an extension per PR runs at a time,
// however many frontends ask. A job whose process exited, or that ran past
// its deadline, no longer counts as running. Only the newest finished job
// of each extension is kept.
func (s *Store) StartExtensionJob(key, name, head string, limit time.Duration) (int64, error) {
	jobs, err := s.extensionJobs(`WHERE key=? AND name=? AND status='running'`, key, name)
	if err != nil {
		return 0, err
	}
	for _, j := range jobs {
		if j.Status == "failed" {
			_, _ = s.db.Exec(`UPDATE extension_jobs SET status='failed', error=?, finished_at=? WHERE id=? AND status='running'`, j.Error, time.Now().Unix(), j.ID)
		}
	}
	now := time.Now()
	res, err := s.db.Exec(`INSERT INTO extension_jobs (key, name, head_oid, status, deadline, started_at)
		SELECT ?, ?, ?, 'running', ?, ? WHERE NOT EXISTS
		(SELECT 1 FROM extension_jobs WHERE key=? AND name=? AND status='running' AND deadline>=?)`,
		key, name, head, now.Add(limit).Unix(), now.Unix(), key, name, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("start %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, fmt.Errorf("%s: %w", name, ErrExtensionRunning)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("start %s: %w", name, err)
	}
	_, err = s.db.Exec(`DELETE FROM extension_jobs WHERE key=? AND name=? AND id<? AND status<>'running'`, key, name, id)
	return id, wrap("start "+name, err)
}

// ExtendExtensionJob moves a running job's deadline to limit from now, for
// when the extension itself starts after the job's setup.
func (s *Store) ExtendExtensionJob(id int64, limit time.Duration) error {
	_, err := s.db.Exec(`UPDATE extension_jobs SET deadline=? WHERE id=? AND status='running'`, time.Now().Add(limit).Unix(), id)
	return wrap("extend extension job", err)
}

func (s *Store) SetExtensionJobPID(id int64, pid int) error {
	_, err := s.db.Exec(`UPDATE extension_jobs SET pid=? WHERE id=?`, pid, id)
	return wrap("set extension pid", err)
}

// CancelExtensionJob asks a running job's agent-run to stop, which ends the
// job. It reports false when the job isn't running.
func (s *Store) CancelExtensionJob(id int64) (bool, error) {
	return s.cancel("job", `SELECT id FROM extension_jobs WHERE id=? AND status='running'`, id)
}

// ExtensionJobCancelled reports whether you cancelled the job.
func (s *Store) ExtensionJobCancelled(id int64) bool { return s.cancelled("job", id) }

// FinishExtensionJob records how the job ended: what the extension printed,
// errMsg when it failed, and how many findings it added to the AI review.
func (s *Store) FinishExtensionJob(id int64, output, errMsg string, added int) error {
	status := "done"
	if errMsg != "" {
		status = "failed"
	}
	_, err := s.db.Exec(`UPDATE extension_jobs SET status=?, output=?, error=?, added=?, finished_at=? WHERE id=?`,
		status, output, errMsg, added, time.Now().Unix(), id)
	return wrap("finish extension", err)
}

// ExtensionJobs are each PR's newest job of each on-demand extension, by PR key.
func (s *Store) ExtensionJobs() (map[string][]*ExtensionJob, error) {
	jobs, err := s.extensionJobs(`WHERE id IN (SELECT max(id) FROM extension_jobs GROUP BY key, name) ORDER BY key, name`)
	if err != nil {
		return nil, err
	}
	out := map[string][]*ExtensionJob{}
	for _, j := range jobs {
		out[j.Key] = append(out[j.Key], j)
	}
	return out, nil
}

// extensionJobs reads jobs. One still running whose process exited, or that
// ran past its deadline, reads as failed.
func (s *Store) extensionJobs(where string, args ...any) ([]*ExtensionJob, error) {
	rows, err := s.db.Query(`SELECT id, key, name, head_oid, status, pid, deadline, started_at, finished_at, output, error, added
		FROM extension_jobs `+where, args...)
	if err != nil {
		return nil, wrap("extension jobs", err)
	}
	defer rows.Close()
	var out []*ExtensionJob
	for rows.Next() {
		var j ExtensionJob
		var deadline, started, finished int64
		if err := rows.Scan(&j.ID, &j.Key, &j.Name, &j.HeadOid, &j.Status, &j.PID, &deadline, &started, &finished, &j.Output, &j.Error, &j.Added); err != nil {
			return nil, wrap("extension jobs", err)
		}
		j.Deadline, j.StartedAt = time.Unix(deadline, 0), time.Unix(started, 0)
		if finished > 0 {
			j.FinishedAt = time.Unix(finished, 0)
		}
		switch {
		case j.Status == "running" && j.PID > 0 && !platform.Alive(j.PID):
			j.Status, j.Error = "failed", "its process exited unexpectedly"
		case j.Status == "running" && time.Now().After(j.Deadline):
			j.Status, j.Error = "failed", "it ran past its timeout"
		}
		out = append(out, &j)
	}
	return out, wrap("extension jobs", rows.Err())
}

// AddFindings adds findings to a review run, one statement each, so a
// finding is never half written.
func (s *Store) AddFindings(id int64, fs []Finding) error {
	for _, f := range fs {
		b, err := json.Marshal(f)
		if err != nil {
			return fmt.Errorf("add findings: %w", err)
		}
		if _, err := s.db.Exec(`UPDATE review_runs SET findings=json_insert(findings, '$[#]', json(?)) WHERE id=?`, string(b), id); err != nil {
			return fmt.Errorf("add findings: %w", err)
		}
	}
	return nil
}

// ExtensionRun is one extension's run on one head of a PR, shared by every
// review of that head. Output is the extension's JSON.
type ExtensionRun struct {
	Key, Name, HeadOid, Status string
	Claim                      string
	Holder                     string
	PID                        int
	Deadline                   time.Time
	StartedAt, FinishedAt      time.Time
	Output, Error              string
}

// Stale is whether a running extension's holder has gone: its process
// exited, or it ran past its deadline.
func (r *ExtensionRun) Stale() bool {
	return r.Status == "running" && (!platform.Alive(r.PID) || time.Now().After(r.Deadline))
}

// ClaimExtension makes pid, the review process of holder, the one that runs
// extension name on head, until limit from now. Only one claim wins: it
// takes a head nobody has claimed, a run whose holder is stale, or a run
// that failed before retryBefore: a new review retries a failure from an
// earlier review, not one from a review that started beside it. Each of those is one conditional statement, so two
// processes claiming at once can't both succeed. It returns the run, which
// is the caller's to run when claimed, or else the current one to wait for
// or to reuse.
func (s *Store) ClaimExtension(key, name, head, holder string, pid int, limit time.Duration, retryBefore time.Time) (run *ExtensionRun, claimed bool, err error) {
	now := time.Now()
	claim := fmt.Sprintf("%d-%d", pid, now.UnixNano())
	deadline := now.Add(limit).Unix()
	res, err := s.db.Exec(`INSERT INTO extension_runs (key, name, head_oid, status, claim, holder, pid, deadline, started_at)
		VALUES (?, ?, ?, 'running', ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, key, name, head, claim, holder, pid, deadline, now.Unix())
	if err != nil {
		return nil, false, fmt.Errorf("claim extension %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		cur, err := s.ExtensionRun(key, name, head)
		if err != nil || cur == nil {
			return cur, false, err
		}
		if !cur.Stale() && !(cur.Status == "failed" && cur.FinishedAt.Before(retryBefore)) {
			return cur, false, nil
		}
		res, err = s.db.Exec(`UPDATE extension_runs SET status='running', claim=?, holder=?, pid=?, deadline=?, started_at=?, finished_at=0, output='', error=''
			WHERE key=? AND name=? AND head_oid=? AND claim=? AND status=?`,
			claim, holder, pid, deadline, now.Unix(), key, name, head, cur.Claim, cur.Status)
		if err != nil {
			return nil, false, fmt.Errorf("claim extension %s: %w", name, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			cur, err = s.ExtensionRun(key, name, head)
			return cur, false, err
		}
	}
	return &ExtensionRun{Key: key, Name: name, HeadOid: head, Status: "running", Claim: claim, Holder: holder, PID: pid,
		Deadline: time.Unix(deadline, 0), StartedAt: time.Unix(now.Unix(), 0)}, true, nil
}

// FinishExtension records how the claim's run ended: its output, or with
// errMsg a failure. It returns false when the claim was lost, to a review
// that took over after the deadline, which then owns the result.
func (s *Store) FinishExtension(run *ExtensionRun, output, errMsg string) (bool, error) {
	status := "done"
	if errMsg != "" {
		status = "failed"
	}
	res, err := s.db.Exec(`UPDATE extension_runs SET status=?, output=?, error=?, finished_at=?
		WHERE key=? AND name=? AND head_oid=? AND claim=? AND status='running'`,
		status, output, errMsg, time.Now().Unix(), run.Key, run.Name, run.HeadOid, run.Claim)
	if err != nil {
		return false, fmt.Errorf("finish extension %s: %w", run.Name, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ReleaseExtension gives up the claim's run unfinished, so a review waiting
// for it claims it and runs it, rather than reuse a run its holder's review
// cut short.
func (s *Store) ReleaseExtension(run *ExtensionRun) error {
	_, err := s.db.Exec(`DELETE FROM extension_runs WHERE key=? AND name=? AND head_oid=? AND claim=? AND status='running'`,
		run.Key, run.Name, run.HeadOid, run.Claim)
	return wrap("release extension "+run.Name, err)
}

// ExtensionRun is extension name's run on head, or nil.
func (s *Store) ExtensionRun(key, name, head string) (*ExtensionRun, error) {
	runs, err := s.extensionRuns(`WHERE key=? AND name=? AND head_oid=?`, key, name, head)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	return runs[0], nil
}

// LastExtensionRun is extension name's newest finished run on the PR at a
// head other than head, the one a re-review starts from, or nil.
func (s *Store) LastExtensionRun(key, name, head string) (*ExtensionRun, error) {
	runs, err := s.extensionRuns(`WHERE key=? AND name=? AND head_oid<>? AND status='done' ORDER BY finished_at DESC, rowid DESC LIMIT 1`, key, name, head)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	return runs[0], nil
}

func (s *Store) extensionRuns(where string, args ...any) ([]*ExtensionRun, error) {
	rows, err := s.db.Query(`SELECT key, name, head_oid, status, claim, holder, pid, deadline, started_at, finished_at, output, error
		FROM extension_runs `+where, args...)
	if err != nil {
		return nil, wrap("extension runs", err)
	}
	defer rows.Close()
	var out []*ExtensionRun
	for rows.Next() {
		var r ExtensionRun
		var deadline, started, finished int64
		if err := rows.Scan(&r.Key, &r.Name, &r.HeadOid, &r.Status, &r.Claim, &r.Holder, &r.PID, &deadline, &started, &finished, &r.Output, &r.Error); err != nil {
			return nil, wrap("extension runs", err)
		}
		r.Deadline, r.StartedAt = time.Unix(deadline, 0), time.Unix(started, 0)
		if finished > 0 {
			r.FinishedAt = time.Unix(finished, 0)
		}
		out = append(out, &r)
	}
	return out, wrap("extension runs", rows.Err())
}

// ---- notes between you and a review agent (local only, never posted)

type Note struct {
	At        time.Time
	Who, Text string
}

func (s *Store) AddNote(key, reviewer, who, text string) error {
	_, err := s.db.Exec(`INSERT INTO review_notes (key, reviewer, at, who, text) VALUES (?,?,?,?,?)`, key, reviewer, time.Now().UnixNano(), who, text)
	return wrap("add note", err)
}

func (s *Store) ClearNotes(key, reviewer string) error {
	_, err := s.db.Exec(`DELETE FROM review_notes WHERE key=? AND reviewer=?`, key, reviewer)
	return wrap("clear notes", err)
}

func (s *Store) Notes(key, reviewer string) ([]Note, error) {
	rows, err := s.db.Query(`SELECT at, who, text FROM review_notes WHERE key=? AND reviewer=? ORDER BY at`, key, reviewer)
	if err != nil {
		return nil, fmt.Errorf("notes: %w", err)
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var e Note
		var at int64
		if err := rows.Scan(&at, &e.Who, &e.Text); err != nil {
			return nil, fmt.Errorf("notes: %w", err)
		}
		e.At = time.Unix(0, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Run is one review run, or nil.
func (s *Store) Run(id int64) (*Run, error) {
	all, err := s.reviews(`WHERE id = ?`, id)
	if err != nil || len(all) == 0 {
		return nil, wrap("run", err)
	}
	return all[0], nil
}

// Runs are a PR's review runs from every agent, newest first.
func (s *Store) Runs(key string) ([]*Run, error) {
	return s.reviews(`WHERE key = ? ORDER BY id DESC`, key)
}

// AllRuns are every PR's review runs, newest first.
func (s *Store) AllRuns() (map[string][]*Run, error) {
	all, err := s.reviews(`ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("all runs: %w", err)
	}
	out := map[string][]*Run{}
	for _, c := range all {
		out[c.Key] = append(out[c.Key], c)
	}
	return out, nil
}

func (s *Store) reviews(where string, args ...any) ([]*Run, error) {
	rows, err := s.db.Query(`SELECT id, key, status, head_oid, session_id, pid, started_at, finished_at,
  activity, activity_at, summary, findings, replies, points, action, action_reason, origin, error, guidance, model, agent, profile FROM review_runs `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("reviews: %w", err)
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		var c Run
		var started, finished, activityAt int64
		var findings, replies, points string
		if err := rows.Scan(&c.ID, &c.Key, &c.Status, &c.HeadOid, &c.SessionID, &c.PID, &started, &finished,
			&c.Activity, &activityAt, &c.Summary, &findings, &replies, &points, &c.Action, &c.ActionReason, &c.Origin, &c.Error, &c.Guidance, &c.Model, &c.Agent, &c.Profile); err != nil {
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

// InboxAt is when SaveInbox last kept the inbox, or zero.
func (s *Store) InboxAt() time.Time {
	var at int64
	if s.db.QueryRow(`SELECT at FROM inbox_cache WHERE id = 1`).Scan(&at) != nil {
		return time.Time{}
	}
	return time.Unix(at, 0)
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

// Shown reports whether FirstTime was asked about name before.
func (s *Store) Shown(name string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM tips WHERE name = ?`, name).Scan(&n)
	return n > 0
}

// Release is what prowl last heard about its own newest release.
type Release struct {
	Checked              time.Time
	Tag, Notes, URL      string
	Installed, Attempted string
}

// Release is the newest release prowl knows of; zero before the first check.
func (s *Store) Release() (Release, error) {
	var r Release
	var at int64
	err := s.db.QueryRow(`SELECT checked_at, tag, notes, url, installed, attempted FROM releases WHERE id = 1`).
		Scan(&at, &r.Tag, &r.Notes, &r.URL, &r.Installed, &r.Attempted)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, nil
	}
	if err != nil {
		return Release{}, wrap("release", err)
	}
	r.Checked = time.Unix(at, 0)
	return r, nil
}

// SetRelease records a check for the newest release, keeping what prowl
// installed and attempted.
func (s *Store) SetRelease(r Release) error {
	_, err := s.db.Exec(`INSERT INTO releases (id, checked_at, tag, notes, url) VALUES (1, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET checked_at = excluded.checked_at, tag = excluded.tag, notes = excluded.notes, url = excluded.url`,
		r.Checked.Unix(), r.Tag, r.Notes, r.URL)
	return wrap("set release", err)
}

// SetInstalled records the release prowl installed itself.
func (s *Store) SetInstalled(tag string) error {
	_, err := s.db.Exec(`UPDATE releases SET installed = ? WHERE id = 1`, tag)
	return wrap("set installed", err)
}

// SetAttempted records the release prowl tried to install in the
// background, so a failing one isn't downloaded again on every check.
func (s *Store) SetAttempted(tag string) error {
	_, err := s.db.Exec(`UPDATE releases SET attempted = ? WHERE id = 1`, tag)
	return wrap("set attempted", err)
}

// SetWatcher records that the `prowl watch` with process ID pid runs, as of
// at. A pid of 0 clears it.
func (s *Store) SetWatcher(pid int, at time.Time) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO watcher (id, pid, at) VALUES (1, ?, ?)`, pid, at.Unix())
	return wrap("set watcher", err)
}

// Watcher is the process ID `prowl watch` last recorded and when, or 0.
func (s *Store) Watcher() (int, time.Time) {
	var pid, at int64
	_ = s.db.QueryRow(`SELECT pid, at FROM watcher WHERE id = 1`).Scan(&pid, &at)
	return int(pid), time.Unix(at, 0)
}

// SetServiceSum records the service definition prowl last wrote.
func (s *Store) SetServiceSum(sum string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO service (id, sum) VALUES (1, ?)`, sum)
	return wrap("set service sum", err)
}

// ServiceSum is the service definition prowl last wrote, or "".
func (s *Store) ServiceSum() string {
	var sum string
	_ = s.db.QueryRow(`SELECT sum FROM service WHERE id = 1`).Scan(&sum)
	return sum
}

// SlackShare is a PR link posted in Slack.
type SlackShare struct {
	Channel, TS, PRKey, PRID, ChannelName, ThreadTS, User, UserName, Link string
	At                                                                    time.Time
	MyReaction                                                            string // the emoji prowl last set on it
}

// SaveSlackShares keeps shares, updating the ones already kept but not the
// reactions prowl recorded on them.
func (s *Store) SaveSlackShares(shares []SlackShare) error {
	for _, sh := range shares {
		_, err := s.db.Exec(`INSERT INTO slack_shares (channel, ts, pr_key, pr_id, channel_name, thread_ts, user, user_name, at, link)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(channel, ts, pr_key) DO UPDATE SET pr_id=excluded.pr_id, channel_name=excluded.channel_name,
  thread_ts=excluded.thread_ts, user_name=excluded.user_name, link=excluded.link`,
			sh.Channel, sh.TS, sh.PRKey, sh.PRID, sh.ChannelName, sh.ThreadTS, sh.User, sh.UserName, sh.At.Unix(), sh.Link)
		if err != nil {
			return wrap("save slack shares", err)
		}
	}
	return nil
}

// SlackShares are every kept share, newest first.
func (s *Store) SlackShares() ([]SlackShare, error) {
	rows, err := s.db.Query(`SELECT channel, ts, pr_key, pr_id, channel_name, thread_ts, user, user_name, at, link, my_reaction
FROM slack_shares ORDER BY at DESC, ts DESC`)
	if err != nil {
		return nil, wrap("slack shares", err)
	}
	defer rows.Close()
	var out []SlackShare
	for rows.Next() {
		var sh SlackShare
		var at int64
		if err := rows.Scan(&sh.Channel, &sh.TS, &sh.PRKey, &sh.PRID, &sh.ChannelName, &sh.ThreadTS, &sh.User, &sh.UserName, &at, &sh.Link, &sh.MyReaction); err != nil {
			return nil, wrap("slack shares", err)
		}
		sh.At = time.Unix(at, 0)
		out = append(out, sh)
	}
	return out, wrap("slack shares", rows.Err())
}

// SetSlackReaction records the emoji prowl set on a share, "" for none.
func (s *Store) SetSlackReaction(sh SlackShare, emoji string) error {
	_, err := s.db.Exec(`UPDATE slack_shares SET my_reaction = ? WHERE channel = ? AND ts = ? AND pr_key = ?`,
		emoji, sh.Channel, sh.TS, sh.PRKey)
	return wrap("set slack reaction", err)
}

// SetPendingComments keeps your inline comments on the PR key (JSON) until
// your next review. Empty forgets them.
func (s *Store) SetPendingComments(key string, comments []byte) error {
	var err error
	if len(comments) == 0 {
		_, err = s.db.Exec(`DELETE FROM pending_comments WHERE key = ?`, key)
	} else {
		_, err = s.db.Exec(`INSERT OR REPLACE INTO pending_comments (key, comments) VALUES (?, ?)`, key, string(comments))
	}
	return wrap("set pending comments", err)
}

// PendingComments are your inline comments on the PR key (JSON), or nil.
func (s *Store) PendingComments(key string) ([]byte, error) {
	var comments string
	err := s.db.QueryRow(`SELECT comments FROM pending_comments WHERE key = ?`, key).Scan(&comments)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pending comments: %w", err)
	}
	return []byte(comments), nil
}

// PendingKeys are the PRs you have inline comments pending on.
func (s *Store) PendingKeys() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT key FROM pending_comments`)
	if err != nil {
		return nil, wrap("pending keys", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, wrap("pending keys", err)
		}
		out[key] = true
	}
	return out, wrap("pending keys", rows.Err())
}

// DeleteSlackShares forgets the shares of a PR, once it closes.
func (s *Store) DeleteSlackShares(key string) error {
	_, err := s.db.Exec(`DELETE FROM slack_shares WHERE pr_key = ?`, key)
	return wrap("delete slack shares", err)
}

// SlackSearched is when the last search for shares succeeded, or zero.
func (s *Store) SlackSearched() time.Time {
	var at int64
	if s.db.QueryRow(`SELECT at FROM slack_search WHERE id = 1`).Scan(&at) != nil {
		return time.Time{}
	}
	return time.Unix(at, 0)
}

// SetSlackSearched records a search for shares that succeeded.
func (s *Store) SetSlackSearched(at time.Time) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO slack_search (id, at) VALUES (1, ?)`, at.Unix())
	return wrap("set slack searched", err)
}

// SlackSignedIn reports whether `prowl slack` saved a token in the keyring.
func (s *Store) SlackSignedIn() bool {
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM slack_signin`).Scan(&n)
	return n > 0
}

// SetSlackSignedIn records that a token is in the keyring, or no longer is.
func (s *Store) SetSlackSignedIn(on bool) error {
	q := `DELETE FROM slack_signin`
	if on {
		q = `INSERT OR REPLACE INTO slack_signin (id) VALUES (1)`
	}
	_, err := s.db.Exec(q)
	return wrap("set slack signed in", err)
}
