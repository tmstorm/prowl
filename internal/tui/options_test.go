package tui

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/config"
)

// The extensions file the options open reads back as the extensions it was
// made from, every setting kept.
func TestExtensionsText(t *testing.T) {
	c := config.Default()
	c.Agent.Extensions = []config.Extension{
		{Name: "perf", Repos: []string{"acme/web"}},
		{Name: "factory", Command: "acme-factory --pr", Repos: []string{"acme/api", "acme/web"}, Timeout: config.Duration{Duration: 2 * time.Minute},
			NeedsBase: true, OnDemand: true, Key: "f", Confirm: true},
	}
	i := slices.IndexFunc(options, func(o option) bool { return o.key == "extensions" })
	var back config.Config
	if err := options[i].set(&back, options[i].get(&c)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Agent.Extensions, c.Agent.Extensions) {
		t.Fatalf("read back %+v\nwant %+v", back.Agent.Extensions, c.Agent.Extensions)
	}
}

// A key the extensions file doesn't know, from a typo or a block pasted from
// the config, is refused by name rather than dropped.
func TestExtensionsTextRefusesUnknownKeys(t *testing.T) {
	i := slices.IndexFunc(options, func(o option) bool { return o.key == "extensions" })
	for text, want := range map[string]string{
		"[[extensions]]\nname = \"factory\"\ncommand = \"f\"\nrepos = [\"*\"]\non_demnd = true\n": "unknown extensions.on_demnd",
		"[[agent.extensions]]\nname = \"perf\"\nrepos = [\"*\"]\n":                                "unknown agent",
	} {
		var c config.Config
		if err := options[i].set(&c, text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
}

// K on a perf setting opens its own row of the config docs.
func TestPerfOptionTopics(t *testing.T) {
	for key, want := range map[string]string{"perf.must_fix": "[agent.perf]", "perf.bench_commands": "[agent.perf.bench_commands]", "extensions": "[[agent.extensions]]"} {
		if got := optionTopic(key); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
}
