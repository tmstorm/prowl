package docs_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tmstorm/prowl/docs"
)

// The man pages carry every command and setting, with the sections man
// expects, and none of the page's Markdown; groff reads them without a
// warning when it's installed.
func TestWriteMan(t *testing.T) {
	dir := t.TempDir()
	if err := docs.WriteMan(dir, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		b, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cli, cfg := read("man1/prowl.1"), read("man5/prowl-config.5")
	for page, want := range map[string][]string{
		cli: {".TH PROWL 1 \"\" \"prowl 1.2.3\"", ".SH NAME\nprowl \\- ", ".SH SYNOPSIS", ".SH DESCRIPTION", ".SH ENVIRONMENT", ".SH SEE ALSO\nprowl\\-config(5)"},
		cfg: {".TH PROWL-CONFIG 5", ".SH NAME\nprowl\\-config \\- ", ".TS\n", ".TE\n", ".SH SEE ALSO\nprowl(1)"},
	} {
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Errorf("missing %q", w)
			}
		}
		for _, md := range []string{"```", "| --- |", "## "} {
			if strings.Contains(page, md) {
				t.Errorf("Markdown %q left in a man page", md)
			}
		}
	}
	p, _ := docs.Get("cli")
	for _, cmd := range regexp.MustCompile(`(?m)^## (prowl \w+)$`).FindAllStringSubmatch(p.Markdown, -1) {
		if !strings.Contains(cli, ".SH "+strings.ToUpper(cmd[1])) {
			t.Errorf("prowl.1 has no section for %s", cmd[1])
		}
	}
	c, _ := docs.Get("config")
	for _, row := range regexp.MustCompile("(?m)^\\| `([^`]+)`").FindAllStringSubmatch(c.Markdown, -1) {
		if !strings.Contains(cfg, `\fB`+strings.ReplaceAll(row[1], "-", `\-`)+`\fR`) {
			t.Errorf("prowl-config.5 doesn't list %s", row[1])
		}
	}
	if _, err := exec.LookPath("groff"); err != nil {
		return
	}
	for _, path := range []string{"man1/prowl.1", "man5/prowl-config.5"} {
		out, _ := exec.Command("groff", "-t", "-man", "-ww", "-z", filepath.Join(dir, path)).CombinedOutput()
		if len(out) > 0 {
			t.Errorf("groff warns about %s:\n%s", path, out)
		}
	}
}
