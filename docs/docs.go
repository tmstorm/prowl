// Package docs holds prowl's docs pages, built into the binary so prowl can
// show the ones for its own version.
package docs

import "embed"

// Pages are the docs pages, docs/*.md.
//
//go:embed *.md
var Pages embed.FS
