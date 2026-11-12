package agent_test

import (
	"os"
	"testing"

	"github.com/tmstorm/prowl/internal/agent"
)

// Opt-in: PROWL_REAL_AGENT=claude|codex runs one real turn in PROWL_REAL_DIR,
// to check the agent accepts prowl's schema and flags.
func TestRealAgentSchema(t *testing.T) {
	name := os.Getenv("PROWL_REAL_AGENT")
	if name == "" {
		t.Skip("set PROWL_REAL_AGENT to run")
	}
	be := backend(t, name)
	res, sid, err := be.Review(agent.Turn{Dir: os.Getenv("PROWL_REAL_DIR"), Model: os.Getenv("PROWL_REAL_MODEL"),
		Prompt:   "Review the code in this directory for bugs. Read the files first. Report findings with path and line.",
		Activity: func(s string) { t.Log("activity:", s) }, SetModel: func(s string) { t.Log("model:", s) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("session %s: %+v", sid, res)
	if sid == "" || res.Summary == "" {
		t.Fatal("empty review")
	}
}
