package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The packages below the TUI never import it or name its keys, so another
// frontend can use them as they are. The config is the exception for keys:
// its file documents the TUI's own settings.
func TestLayers(t *testing.T) {
	below := []string{"docs", "internal/agent", "internal/config", "internal/core", "internal/gh", "internal/inbox",
		"internal/platform", "internal/skill", "internal/slack", "internal/store"}
	for _, dir := range below {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			for _, imp := range imports(t, f) {
				if imp == "github.com/tmstorm/prowl/internal/tui" {
					t.Errorf("%s imports the TUI", f)
				}
			}
			if dir != "internal/config" && names(t, f, "<leader>") {
				t.Errorf("%s names a TUI key (<leader>); name the action instead", f)
			}
		}
	}
	if names(t, "main.go", "<leader>") {
		t.Error("main.go names a TUI key (<leader>); name the action instead")
	}
}

func imports(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		out = append(out, p)
	}
	return out
}

func names(t *testing.T, path, text string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(b), text)
}
