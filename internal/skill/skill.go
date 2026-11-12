// Package skill ships prowl's prowl-findings skill, which lets the coding
// agent in a checkout (Claude Code, Codex) act on prowl's review findings.
package skill

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed SKILL.md
var Content []byte

const Name = "prowl-findings"

// Dir is where the skill goes for an agent: Claude Code's user skills by
// default, or Codex's with agent "codex".
func Dir(agent string) string {
	home, _ := os.UserHomeDir()
	if agent == "codex" {
		base := os.Getenv("CODEX_HOME")
		if base == "" {
			base = filepath.Join(home, ".codex")
		}
		return filepath.Join(base, "skills", Name)
	}
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		base = filepath.Join(home, ".claude")
	}
	return filepath.Join(base, "skills", Name)
}

// Install writes the skill into dir and returns the path.
func Install(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("install: %w", err)
	}
	path := filepath.Join(dir, "SKILL.md")
	return path, os.WriteFile(path, Content, 0o644)
}
