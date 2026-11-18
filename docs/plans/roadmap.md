# Roadmap

The PRs after built-in docs (`builtin-docs.md`), in order. Each is one PR, split into commits.

1. Extensions
2. Findings for the author
3. Performance review, the first extension
4. Before 1.0: the VS Code extension, a stable core, then packages

## 1. Extensions

A review step anyone can add without changing prowl: a command prowl runs during every review of the repos it's enabled for, whose output goes to the reviewer and into the findings.

### Decisions

- An extension is any executable, configured by name. prowl talks to it in JSON over stdin and stdout, as it already does with the `command` agent.
- prowl's own extensions implement the same Go interface as an external one, behind it the exec adapter. Performance review is built in this way, so the interface carries a real extension from the first release.
- An extension that fails or runs out of time doesn't stop the review. The review summary says which one and why.
- Opt-in per repo, like everything that runs code.

### Config

```toml
[[agent.extensions]]
name = "perf"                     # built in: no command
repos = ["tmstorm/prowl", "acme/web"]

[[agent.extensions]]
name = "licenses"
command = "acme-license-check"
repos = ["*"]
timeout = "2m"                    # default 10m
```

### Protocol

prowl runs each enabled extension after it has the checkouts and before it builds the prompt, in parallel. Stdin:

```json
{"repo": "acme/web", "head_dir": "...", "base_dir": "...", "head": "<sha>", "base": "<sha>",
 "since": "<sha of the last review, or empty>", "changed_files": ["src/a.ts"], "run_image": "node:24"}
```

Stdout:

```json
{"prompt": "markdown for a section of the review prompt",
 "findings": [{"severity": "should-fix", "title": "...", "body": "...", "rule": "...", "path": "", "line": 0}],
 "summary": "one line for the review summary"}
```

A finding with no path goes to the reviewer, who must attach it to the line in the diff that causes it and may not lower its severity. A finding with a path goes into the review as is. `base_dir` is a worktree at the merge base that prowl adds only when an extension asks for it with `"needs_base": true` in its config.

### On demand

An extension can also run when you ask, on the PR you're looking at, instead of on every review:

```toml
[[agent.extensions]]
name = "factory"
command = "acme-factory"
repos = ["acme/api"]
on_demand = true
key = "f"                         # optional: <leader>xf
```

- prowl owns the keys. Every on-demand extension is listed by name under one leader group, `<leader>x`, and `key` only picks its letter inside that group, so an extension can't take a key prowl uses or will use. A `key` that two extensions share is a config error.
- Same protocol. prowl prepares the same checkouts and input as for a review. The output shows in a popup: `summary`, then `prompt` rendered as markdown, then the findings. Findings with a path join the PR's AI review as drafts you edit before posting. The rest stay in the popup.
- `on_demand = true` takes the extension out of the automatic run. One that should do both is listed twice.
- The action lives in `internal/core`, so the VS Code extension gets the same list as command-palette entries.

### Code

- `internal/config`: `Extensions`, with a lookup per repo, `on_demand`, `key` and the duplicate-key check.
- `internal/agent`: the `Extension` interface, the exec adapter, running them with their timeouts, the prompt sections, the locked-severity findings.
- `internal/core`: running one extension on a PR on demand.
- `internal/tui`: the `<leader>x` group, the output popup.
- Docs: a section in `docs/ai-reviews.md` with the protocol, `docs/config.md`, the `<leader>x` keys in `docs/reviewing.md`.
- Tests: an extension script's prompt and findings reach the review, a failing and a slow one leave the review going with a note in the summary, a finding with no path is attached by the reviewer at its severity, a repo the extension isn't enabled for is unchanged, an on-demand extension stays out of reviews and shows under `<leader>x` with its key, a shared key fails config loading.

Open: an on-demand extension that acts on the PR (starts a job, pushes, posts) runs something with side effects on a keypress. Whether prowl asks before running it, and what it shows when it fails halfway, depends on what extensions like the forked one do. Settle it before this PR.

## 2. Findings for the author

When someone reviews your PR on GitHub, `prowl findings` in your checkout shows that review, so you or your agent can work through it like a local AI review. Today it only reads the local store and says "no finished review" on your own PR even after changes were requested.

### Decisions

- No new command or skill. `prowl findings` gets a GitHub source, and the `prowl-findings` skill learns the author side.
- Read-only. The agent drafts thread replies and the author posts them.
- prowl tags each finding it posts with a hidden HTML comment, so the author's prowl can tell AI findings from hand-written comments and sort by severity.

### The marker

`findingBody` (`internal/core/post.go`) appends `<!-- prowl:finding v=1 severity=must-fix rule="AGENTS.md: wrap errors" -->`, and each PR-level finding in the review body gets one at the end of its bullet. Your own pending comments get none. Reviews posted before the marker start with `**must-fix:** Title`, and the parser falls back to that.

### `prowl findings` on your own PR

When the current branch's PR (`resolvePR`) is yours, prowl fetches it with `FetchDetail` and prints, after any local AI review:

- Each reviewer's latest verdict, changes requested first.
- Their latest review bodies, PR-level findings split out.
- Unresolved threads: path, line, hunk, who opened it, replies, whether it's outdated, thread ID.
- Sorted: tagged must-fix, hand-written comments in a changes-requested review, tagged should-fix, other hand-written comments, nits. `suggestion` blocks as the patch they'd apply.

Bots and resolved threads are left out; `--all` brings resolved threads back. `--json` prints `verdicts` and `items` (`source`, `kind`, `thread_id`, `path`, `line`, `severity`, `author`, `outdated`, `body`, `suggestion`). A PR someone else wrote keeps today's behaviour.

### The skill

`internal/skill/SKILL.md` adds the author-side triggers ("changes requested on my PR", "address the review comments"). A hand-written comment is a person asking, so the agent fixes it or drafts a reply saying why not, never drops it. Each thread gets a drafted reply listed with its thread ID. The no-posting rule stays.

### Code

- `internal/core/post.go`: the marker.
- `internal/agent`: parsing it, the GitHub items beside `FindingsMarkdown`, the sort.
- `main.go`: `findings` fetches the detail on your own PR, `--json`.
- `internal/skill/SKILL.md`, `skill_test.go` if it pins the text. Docs: `docs/ai-reviews.md`, the `prowl findings` usage text.
- Tests: marker round trip, the `**must-fix:**` fallback, sort order, `--json` fields, someone else's PR unchanged.

Open: sorting and colouring tagged threads by severity in the TUI's PR view. Cheap once parsing exists, but UI scope beyond `findings`.

## 3. Performance review

The built-in `perf` extension. For a repo it's enabled on, prowl measures the repo's benchmarks on the base and the head and reports regressions as findings, and the reviewer looks for performance problems in the change and proposes fixes even where no benchmark covers the code. It measures and proposes, never applies.

### Decisions

- Any language from the start: prowl runs the repo's bench command and does the statistics itself.
- prowl decides whether a regression is real and how severe it is. Numbers an agent read off a terminal aren't evidence.
- prowl ships benchmarks for its own hot paths in this PR and enables `perf` on itself.
- Announced: the `feat(agent)` commit shows in the TUI's what's-new tip, and `prowl review` prints a one-time hint (`FirstTime` per repo) in a repo without `perf`, so agents that only use the CLI see it too.

### Config

```toml
[agent.perf]
bench_commands = { "acme/web" = "pnpm bench" }
must_fix = 20   # percent; 0 means a regression is never must-fix
```

A Go repo with no bench command uses `go test -run '^$' -bench . -benchmem {dirs}`. Any other language without one gets the proposals only. Measuring also needs a run image for the repo.

### Measuring

1. Run the bench command 10 times on the base and 10 on the head, interleaved, in the run image. Interleaving spreads machine noise across both sides.
2. Read the output into one sample per benchmark and unit per run.
3. Compare per benchmark and unit with `golang.org/x/perf/benchmath`, the library behind `benchstat`: the median change and a Mann-Whitney U test.

prowl reads three line formats and times the whole command when none appear:

- Go benchmark format, with `golang.org/x/perf/benchfmt`. The others are converted to it.
- Rust's `bencher` lines (libtest, and criterion with `-- --output-format bencher`).
- The C++ benchmark library's console lines (`BM_Sort  1234 ns  1230 ns  1000`).

No JSON parsers: reports go to tool-specific paths and change between major versions. One number per benchmark per run is enough, so a `jq` line turns pytest-benchmark, BenchmarkDotNet or vitest JSON into Go-format lines. A benchmark only on the head is reported as new.

### Narrowing to the change

The changed files come from the extension input, the same set for both sides. `{dirs}` in the command expands to their directories that exist on that side, and measuring is skipped when it's empty. `PROWL_CHANGED_FILES` lists the files for a script that maps them to benchmarks itself. prowl never passes a git ref: tools that work out what's affected from git select nothing in the base worktree.

| Language | Tool | Command |
|---|---|---|
| Go | `testing` | built in: `go test -run '^$' -bench . -benchmem {dirs}` |
| TS/JS | vitest bench | `vitest bench --run {dirs}`, then `jq` over `--reporter=json` |
| Python | pytest-benchmark | `pytest --benchmark-only --benchmark-json=out.json {dirs}`, then `jq` |
| C# | BenchmarkDotNet | a script: `--filter` from the changed files, `--exporters json --job short`, then `jq` |
| C++ | benchmark library | a script: `--benchmark_filter` from the changed files |
| Rust | criterion or libtest | a script: `cargo bench -p <changed crates> -- --output-format bencher` |
| GDScript | none standard | a script under `godot --headless -s` that prints Go-format lines, or wall time |

### Thresholds

A regression is significant (p < 0.05, benchstat's default; 10 runs a side needs a consistent shift) and at least 5% worse in sec/op, B/op or allocs/op. It's should-fix, and must-fix past `must_fix` percent. Improvements past the same bar go in the summary.

### The reviewer

The extension's prompt section has the benchstat table and the regressions, which come back as findings with no path for the reviewer to attach. It also asks the reviewer to look in the changed code for work that grows faster than it needs to (a call per item in a loop, a quadratic search, allocation in a hot loop, unbounded goroutines or buffers, a missing index) and propose a fix. An unmeasured proposal is at most should-fix, has the rule `perf: unmeasured`, and includes a benchmark in the repo's style. Once the author adds it, the next review measures it. On a re-review only what changed since the last one is measured, as `testWhatChanged` does for tests.

### Time

The extension's `timeout` (10 minutes) is a guess. prowl stores how long each measurement took and prints it in the summary ("benchmarks: 3m12s, 4 compared"), so the default can come from real reviews.

### prowl's own benchmarks

`parseDiff` and `renderDiffLine` on a 5,000-line diff (`internal/tui/diff.go`), highlighting it (`highlight.go`), rendering a long conversation (`markdown.go`), `inbox.Classify` on 500 PRs. Fixtures generated with made-up repos and logins. `task bench` runs them.

### Code

- `internal/config`: `Perf` (`BenchCommands`, `MustFix`).
- `internal/agent`: the `perf` extension (base worktree, interleaved runs, `{dirs}`, the parsers, `benchmath`, thresholds, prompt section), the duration on the run in `internal/store`.
- `main.go`: the one-time hint. `go.mod`: `golang.org/x/perf`.
- Docs: `docs/ai-reviews.md` with a recipe per language, `docs/config.md`.
- Tests: thresholds on fixed samples (significant but small, big but noisy, past `must_fix`), each parser on captured output, the wall-time fallback, `{dirs}` with a directory missing on one side and with nothing changed, a benchmark only on the head, the hint shown once.

Open: a built-in `performance` profile with the same lens for a one-off review of a repo without `perf`. I'd leave it out; enabling is one line.

## 4. Before 1.0

Last, once the features above have settled:

- The VS Code extension, released for people to try. What it needs from `internal/core` goes into the core before the API is settled.
- A stable core: settle `internal/core`'s API, and the extension protocol with it, so other interfaces and extensions can rely on it.
- Packages: AUR (`prowl-bin`), then Homebrew and Scoop.

## Being considered

Not scheduled. Each needs a reason from using prowl before it gets a place above.

- Ask prowl: a chat about prowl itself, opened from anywhere, with only the `docs` tool and a read-only view of the inbox (each PR's bucket and why, its CI and review state). It would answer "why is api#12 under re-check?" or "what's blocking the oldest PR waiting on me?", which the review chat can't: it only exists after a review, and can't see the inbox. It needs an agent session without a checkout, an inbox tool for the tool server, and a place in the UI.
