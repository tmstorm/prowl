package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/platform"
)

// Extension is a review step prowl runs beside the reviewer: an external
// program, or one built into prowl behind the same protocol.
type Extension interface {
	Name() string
	Run(ctx context.Context, in ExtensionInput) (ExtensionOutput, error)
}

// ExtensionInput is what an extension gets on stdin. Since, ChangedSince and
// Previous come from its own last run on the PR, and are empty on its first.
type ExtensionInput struct {
	Repo         string             `json:"repo"`
	HeadDir      string             `json:"head_dir"`
	BaseDir      string             `json:"base_dir,omitempty"`
	Head         string             `json:"head"`
	Base         string             `json:"base"`
	ChangedFiles []string           `json:"changed_files"`
	RunImage     string             `json:"run_image,omitempty"`
	Since        string             `json:"since,omitempty"`
	ChangedSince []string           `json:"changed_since,omitempty"`
	Previous     []ExtensionFinding `json:"previous,omitempty"`
}

// ExtensionOutput is what an extension prints on stdout. Prompt becomes a
// section of the review prompt, and Summary a line of the review summary.
type ExtensionOutput struct {
	Prompt   string             `json:"prompt"`
	Findings []ExtensionFinding `json:"findings"`
	Summary  string             `json:"summary"`
}

// ExtensionFinding is a finding in the extension protocol. One with no Path
// goes to the reviewer to attach to the line that causes it.
type ExtensionFinding struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Rule     string `json:"rule,omitempty"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
}

var extensionSeverities = []string{"must-fix", "should-fix", "nit"}

// ExtensionFor is the extension that e configures.
func ExtensionFor(e config.Extension) Extension { return execExtension{e} }

// execExtension runs the configured command in the PR's worktree, the input
// as JSON on stdin and the output as JSON on stdout.
type execExtension struct{ cfg config.Extension }

func (e execExtension) Name() string { return e.cfg.Name }

func (e execExtension) Run(ctx context.Context, in ExtensionInput) (ExtensionOutput, error) {
	var out ExtensionOutput
	stdin, err := json.Marshal(in)
	if err != nil {
		return out, fmt.Errorf("%s: %w", e.cfg.Name, err)
	}
	ctx, cancel := context.WithTimeout(ctx, e.cfg.Limit())
	defer cancel()
	cmd := shell(ctx, e.cfg.Command)
	platform.KillGroup(cmd)
	cmd.Dir = in.HeadDir
	cmd.Env = CleanEnv()
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout bytes.Buffer
	var stderr tail
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after %s", e.cfg.Limit())
		}
		return out, fmt.Errorf("%s: %w%s", e.cfg.Name, err, lastLines(stderr.String(), 20))
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return out, fmt.Errorf("%s: its output isn't the extension JSON: %w", e.cfg.Name, err)
	}
	for i, f := range out.Findings {
		if !slices.Contains(extensionSeverities, f.Severity) {
			return out, fmt.Errorf("%s: finding %d has severity %q; use %s", e.cfg.Name, i+1, f.Severity, strings.Join(extensionSeverities, ", "))
		}
	}
	return out, nil
}

// lastLines is the end of s, n lines at most, on lines of its own after the
// error it explains, or "" when s is empty.
func lastLines(s string, n int) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return "\n" + strings.Join(lines[max(0, len(lines)-n):], "\n")
}
