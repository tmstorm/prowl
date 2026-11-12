package store_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

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
	_ = st.FinishReview(a, "s1", "first", "", []store.Finding{{Title: "x", Excluded: true}})
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
