package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/store"
)

// Each new review is a run, listed newest first, and closing the PR removes
// them.
func TestRunsAndPrune(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, _ := st.StartRun("o/r#1", "h1", "claude", "", 0)
	_ = st.FinishRun(a, "s1", "first", "", []store.Finding{{Title: "x", Excluded: true}})
	b, _ := st.StartRun("o/r#1", "h2", "codex", "", 0)
	c, _ := st.StartRun("o/r#1", "h2", "claude", "", 0)
	runs, _ := st.Runs("o/r#1")
	if len(runs) != 3 || runs[0].ID != c {
		t.Fatalf("runs = %d, newest %d", len(runs), runs[0].ID)
	}
	if runs[1].ID != b {
		t.Fatalf("runs out of order: %+v", runs)
	}
	_ = st.AddNote("o/r#1", "codex", "you", "hi")
	if log, _ := st.Notes("o/r#1", "claude"); len(log) != 0 {
		t.Fatal("notes belong to one agent")
	}
	if keys, _ := st.ReviewedPRs(); len(keys) != 1 || keys[0] != "o/r#1" {
		t.Fatalf("reviewed PRs = %v", keys)
	}
	_ = st.DeleteReviews("o/r#1")
	if runs, _ := st.Runs("o/r#1"); len(runs) != 0 {
		t.Fatal("DeleteReviews should remove the runs")
	}
	if log, _ := st.Notes("o/r#1", "codex"); len(log) != 0 {
		t.Fatal("and the notes")
	}
}

// The data folder holds private PR text and review findings, so only you can
// read it.
func TestDataDirIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix modes")
	}
	dir := filepath.Join(t.TempDir(), "prowl")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode: %v %v", fi.Mode(), err)
	}
}

// Shares are kept once per message and PR, newest first, until the PR closes,
// a save keeps the reaction prowl set, and the last search survives a restart.
func TestSlackShares(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := store.SlackShare{Channel: "C1", TS: "1.0", PRKey: "o/r#1", PRID: "PR1", ChannelName: "reviews", User: "U1", UserName: "alice", At: time.Unix(100, 0),
		Link: "https://acme.slack.com/archives/C1/p1"}
	newer := old
	newer.TS, newer.At = "2.0", time.Unix(200, 0)
	other := store.SlackShare{Channel: "D1", TS: "3.0", PRKey: "o/r#2", PRID: "PR2", User: "U2", UserName: "bob", At: time.Unix(150, 0)}
	if err := st.SaveSlackShares([]store.SlackShare{old, newer, other, old}); err != nil {
		t.Fatal(err)
	}
	got, err := st.SlackShares()
	if err != nil || len(got) != 3 || got[0].TS != "2.0" || got[1].PRKey != "o/r#2" || got[2] != old {
		t.Fatalf("shares = %+v, %v", got, err)
	}
	_ = st.SetSlackReaction(got[0], "eyes")
	_ = st.SaveSlackShares([]store.SlackShare{newer})
	if got, _ := st.SlackShares(); got[0].MyReaction != "eyes" || got[1].MyReaction != "" {
		t.Fatalf("reaction not kept through a save: %+v", got)
	}
	_ = st.DeleteSlackShares("o/r#1")
	if got, _ := st.SlackShares(); len(got) != 1 || got[0].PRKey != "o/r#2" {
		t.Fatalf("after delete: %+v", got)
	}
	if !st.SlackSearched().IsZero() {
		t.Fatal("searched before any search")
	}
	_ = st.SetSlackSearched(time.Unix(300, 0))
	st.Close()
	st, _ = store.Open(dir)
	defer st.Close()
	if !st.SlackSearched().Equal(time.Unix(300, 0)) {
		t.Fatalf("searched = %v", st.SlackSearched())
	}
}

// prowl remembers that a Slack token is in the keyring until you sign out.
func TestSlackSignedIn(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.SlackSignedIn() {
		t.Fatal("signed in before signing in")
	}
	_ = st.SetSlackSignedIn(true)
	_ = st.SetSlackSignedIn(true)
	if !st.SlackSignedIn() {
		t.Fatal("not signed in after signing in")
	}
	_ = st.SetSlackSignedIn(false)
	if st.SlackSignedIn() {
		t.Fatal("still signed in after signing out")
	}
}

// A database an older prowl made opens, keeps what it held and works, and
// opens again.
func TestOldDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "prowl.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE pr_state (key TEXT PRIMARY KEY, bucket INTEGER NOT NULL, reason TEXT NOT NULL,
  head_oid TEXT NOT NULL, title TEXT NOT NULL, url TEXT NOT NULL, seen_at INTEGER NOT NULL);
INSERT INTO pr_state VALUES ('o/r#1', 2, 're-requested', 'abc', 'fix: one', 'https://github.com/o/r/pull/1', 1)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := st.AllStates(); err != nil || got["o/r#1"] != (store.PRState{Key: "o/r#1", Bucket: 2, Reason: "re-requested", HeadOid: "abc"}) {
			t.Fatalf("states = %+v, %v", got, err)
		}
		if err := st.ReplaceStates([]store.PRState{{Key: "o/r#1", Bucket: 2, Reason: "re-requested", HeadOid: "abc"}}); err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
}
