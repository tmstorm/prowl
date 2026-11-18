// Package docs holds prowl's docs pages, built into the binary so prowl can
// show the ones for its own version.
package docs

import "embed"

// Pages are the docs pages. install.md isn't one: it's for before prowl is
// installed, so it lives on GitHub only.
//
//go:embed inbox.md reviewing.md ai-reviews.md slack.md tui.md cli.md config.md
var Pages embed.FS
