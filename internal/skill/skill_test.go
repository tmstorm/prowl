package skill_test

import (
	"github.com/tmstorm/prowl/internal/skill"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillFrontmatterAndInstall(t *testing.T) {
	s := string(skill.Content)
	if !strings.HasPrefix(s, "---\nname: "+skill.Name+"\ndescription: ") {
		t.Fatalf("SKILL.md frontmatter must start with name %q and a description", skill.Name)
	}
	for _, must := range []string{"prowl findings", "Do not commit, push, or post"} {
		if !strings.Contains(s, must) {
			t.Errorf("skill lost %q", must)
		}
	}
	dir := t.TempDir()
	path, err := skill.Install(filepath.Join(dir, skill.Name))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != s {
		t.Fatal("installed skill differs from the embedded one")
	}
}
