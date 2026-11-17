package store_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/store"
)

// Each new review is a run; the latest per agent is current, earlier ones are
// history, and closing the PR removes them.
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
	cur := store.Current(runs)
	if len(cur) != 2 || cur[0].ID != c || cur[1].ID != b {
		t.Fatalf("current runs wrong: %+v", cur)
	}
	if store.Latest(runs, "claude").ID != c {
		t.Fatal("latest claude run should be the newest")
	}
	_ = st.AddReviewLog("o/r#1", "codex", "you", "hi")
	if log, _ := st.ReviewLog("o/r#1", "claude"); len(log) != 0 {
		t.Fatal("notes belong to one agent")
	}
	if keys, _ := st.ReviewedPRs(); len(keys) != 1 || keys[0] != "o/r#1" {
		t.Fatalf("reviewed PRs = %v", keys)
	}
	_ = st.DeleteReviews("o/r#1")
	if runs, _ := st.Runs("o/r#1"); len(runs) != 0 {
		t.Fatal("DeleteReviews should remove the runs")
	}
	if log, _ := st.ReviewLog("o/r#1", "codex"); len(log) != 0 {
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
	old := store.SlackShare{Channel: "C1", TS: "1.0", PRKey: "o/r#1", PRID: "PR1", ChannelName: "reviews", User: "U1", UserName: "alice", At: time.Unix(100, 0)}
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
