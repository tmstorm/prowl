package store_test

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
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

// Of many processes claiming one extension run at once, one wins and the
// rest see its run: on a head nobody claimed, and on one whose holder exited.
func TestClaimExtensionOnce(t *testing.T) {
	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Skip("needs true")
	}
	for _, stale := range []bool{false, true} {
		dir := t.TempDir()
		if stale {
			st, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			_, _, _ = st.ClaimExtension("o/r#1", "perf", "h1", "gone", exited.Process.Pid, time.Hour, time.Time{})
			st.Close()
		}
		const n = 16
		won := make(chan bool, n)
		var wg sync.WaitGroup
		for i := range n {
			st, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			wg.Go(func() {
				run, claimed, err := st.ClaimExtension("o/r#1", "perf", "h1", fmt.Sprint("reviewer", i), os.Getpid(), time.Minute, time.Now())
				if err != nil || run == nil || run.Status != "running" {
					t.Errorf("claim %d: %+v %v", i, run, err)
				}
				won <- claimed
			})
		}
		wg.Wait()
		close(won)
		wins := 0
		for w := range won {
			if w {
				wins++
			}
		}
		if wins != 1 {
			t.Fatalf("stale=%v: %d claims won", stale, wins)
		}
	}
}

// A run whose holder exited, or ran past its deadline, goes to the next
// claim, and the old holder's result no longer counts.
func TestClaimExtensionTakesOver(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Skip("needs true")
	}
	for _, tc := range []struct {
		name  string
		pid   int
		limit time.Duration
	}{
		{"exited", exited.Process.Pid, time.Hour},
		{"past deadline", os.Getpid(), -time.Second},
	} {
		head := tc.name
		old, claimed, err := st.ClaimExtension("o/r#1", "perf", head, "claude", tc.pid, tc.limit, time.Time{})
		if err != nil || !claimed {
			t.Fatalf("%s: first claim %v %v", tc.name, claimed, err)
		}
		next, claimed, err := st.ClaimExtension("o/r#1", "perf", head, "codex", os.Getpid(), time.Hour, time.Time{})
		if err != nil || !claimed || next.Holder != "codex" {
			t.Fatalf("%s: takeover %+v %v %v", tc.name, next, claimed, err)
		}
		if ok, err := st.FinishExtension(old, `{"summary":"late"}`, ""); ok || err != nil {
			t.Fatalf("%s: the old holder finished: %v %v", tc.name, ok, err)
		}
		if ok, _ := st.FinishExtension(next, `{"summary":"ok"}`, ""); !ok {
			t.Fatalf("%s: the new holder couldn't finish", tc.name)
		}
		if r, _ := st.ExtensionRun("o/r#1", "perf", head); r.Status != "done" || r.Output != `{"summary":"ok"}` {
			t.Fatalf("%s: %+v", tc.name, r)
		}
	}
}

// A finished run is reused. A failed one is claimed again only by a review
// that started after it failed, not by a revise or a review that started
// beside the one that failed. A re-review starts from the newest finished run
// at another head.
func TestClaimExtensionReuse(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	claim := func(head string, retryBefore time.Time) (*store.ExtensionRun, bool) {
		t.Helper()
		r, claimed, err := st.ClaimExtension("o/r#1", "perf", head, "claude", os.Getpid(), time.Hour, retryBefore)
		if err != nil {
			t.Fatal(err)
		}
		return r, claimed
	}
	later := time.Now().Add(time.Hour)
	r, _ := claim("h1", time.Time{})
	_, _ = st.FinishExtension(r, `{"summary":"h1"}`, "")
	if r, claimed := claim("h1", later); claimed || r.Status != "done" {
		t.Fatalf("a done run was claimed again: %+v", r)
	}
	r, _ = claim("h2", time.Time{})
	_, _ = st.FinishExtension(r, "", "exit status 1")
	if r, claimed := claim("h2", time.Time{}); claimed || r.Status != "failed" || r.Error != "exit status 1" {
		t.Fatalf("a revise claimed a failed run: %+v", r)
	}
	if r, claimed := claim("h2", r.StartedAt); claimed {
		t.Fatalf("a review that started beside the failed one retried it: %+v", r)
	}
	if _, claimed := claim("h2", later); !claimed {
		t.Fatal("a new review couldn't retry a failed run")
	}
	if last, _ := st.LastExtensionRun("o/r#1", "perf", "h3"); last == nil || last.HeadOid != "h1" {
		t.Fatalf("last run: %+v", last)
	}
	_ = st.DeleteReviews("o/r#1")
	if r, _ := st.ExtensionRun("o/r#1", "perf", "h1"); r != nil {
		t.Fatal("extension runs outlived the PR")
	}
}
