# Contributing

Thanks for helping. These rules apply to everyone, people and coding agents alike.

Your pull request will be reviewed with prowl, of course. The owl reads every line, and then a person does too.

## Building and testing

You need Go (the version in `go.mod`), `git` and the [GitHub CLI](https://cli.github.com).

```sh
go build              # or: task build
go test -race ./...   # or: task test, which also runs go vet
```

`task install` builds into `~/.local/bin`. Tests never touch GitHub or a real agent: `internal/ghtest` fakes the GitHub API, and the TUI tests drive the whole app through `internal/tui/harness_test.go`. Start there when you test a change to the interface.

CI runs the full suite on Linux and macOS. On Windows it runs the packages that don't need shell scripts, so a change that only works on one system shows up there.

## Stay on task

- Do the task in front of you. A new idea becomes an issue, not part of the current pull request.
- Build for today. No option, abstraction or wrapper until something needs it, and no wrapper used once.
- Keep changes small. Remove more lines than you add when you can.
- Match the surrounding code. No new file or package when the change fits in an existing one, and no file so big it is hard to read.
- `internal/core` holds the behavior and has no terminal code. The TUI calls it; it never reaches past it.
- Nothing specific to one user, team or organization goes in the code or the defaults. That belongs in the user's config.

## Code

- Idiomatic Go: the package name matches its directory, errors are wrapped with context, `context.Context` comes first and is never stored.
- Specific, consistent names. Avoid generic ones like `data` or `manager`, and don't rename the same thing between layers.
- No comments inside function bodies. Doc comments stay short and explain the why the code can't show.
- Test exported behavior, not internals, in a test beside the code. Test-only code never reaches the binary.
- A change comes with the test that proves it.

## Commits

Commits follow [Conventional Commits](https://www.conventionalcommits.org). release-please builds the releases and the changelog from them, so the message is what users read.

```
<type>(<optional scope>)<optional !>: <description>
```

| Type | For |
| --- | --- |
| `feat` | a new feature |
| `fix` | a bug fix |
| `docs` | documentation only |
| `deps` | dependency updates |
| `style` | formatting, no code change |
| `refactor` | a change that neither fixes a bug nor adds a feature |
| `perf` | a performance improvement |
| `test` | tests only |
| `build` | the build, GoReleaser, install scripts |
| `ci` | the workflows |
| `chore` | anything else that doesn't touch the binary |
| `revert` | reverts an earlier commit |

- Lowercase and imperative, describing the change a user sees: `fix(inbox): keep drafts out of needs review`, not `Fixed bug`.
- A breaking change takes `!` after the type or scope, and a `BREAKING CHANGE:` footer saying what to do about it.
- Scopes are the area touched, such as `inbox`, `diff`, `agent`, `config` or `local`.

## Pull requests

- One change per pull request. Don't stack pull requests. Split the steps into commits instead.
- The title uses the commit format. Pull requests are squashed, so the title becomes the commit and the changelog line.
- The description says what changed and why, in prose a reviewer can follow without the diff.
- Docs change with the code. A new key, setting or command updates the README or `docs/` in the same pull request, and `?` in the app lists every key.
- Keep it reviewable in one sitting.
- A change you can see in the interface re-records the README's GIF and screenshots: `vhs docs/demo/demo.tape`. They come from made-up data in `internal/ghtest/demo.go`, so they never show a real account.
