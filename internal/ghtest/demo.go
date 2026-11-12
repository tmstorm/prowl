package ghtest

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/store"
)

// DemoViewer is who the demo is logged in as.
const DemoViewer = "sam"

const (
	demoAuthKey = "nimbus/api#142"
	demoAuthOid = "8f3c2a91d4e7b6058a1f9c3d2e4b7a6f1c0d9e8b"
)

// Demo is a fake GitHub for the made-up nimbus org, for screenshots and
// recordings: PRs waiting on you in every way prowl sorts them, with
// descriptions, threads, CI and diffs.
func Demo() *GitHub {
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	h := time.Hour

	auth := demoPR("api", 142, "feat(auth): rotate refresh tokens on every use", "alice", ago(26*h), demoAuthOid, "SUCCESS", 212, 48, 3)
	demoRequest(&auth, DemoViewer, ago(26*h))
	demoReview(&auth, "bob", "COMMENTED", ago(5*h), demoAuthOid, "")

	cart := demoPR("web", 88, "fix(checkout): keep the cart when a session expires", "priya", ago(50*h), "c41d9e07a2b35f6e8d1c0b9a7e6f5d4c3b2a1908", "SUCCESS", 64, 12, 4)
	demoReview(&cart, DemoViewer, "CHANGES_REQUESTED", ago(30*h), "77aa01bc", "The cart is still dropped when the refresh fails. See the thread on `useCart.ts`.")
	demoCommit(&cart, ago(3*h))
	demoCommit(&cart, ago(2*h))

	logs := demoPR("cli", 57, "feat(logs): add --follow to nimbus logs", "marco", ago(7*h), "2b9e4f60c1d8a7e3f5b2c9d0e1a4f6b8c7d3e2a1", "FAILURE", 131, 9, 3)
	demoRequest(&logs, DemoViewer, ago(7*h))

	limits := demoPR("api", 144, "docs: document rate limits per plan", "jordan", ago(74*h), "e5d4c3b2a1f0e9d8c7b6a5f4e3d2c1b0a9f8e7d6", "SUCCESS", 41, 3, 1)
	demoRequest(&limits, DemoViewer, ago(74*h))

	batch := demoPR("api", 139, "perf(db): batch session lookups", "bob", ago(52*h), "a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a1b0", "PENDING", 97, 61, 5)
	demoReview(&batch, DemoViewer, "APPROVED", ago(20*h), "1c2d3e4f", "")
	demoCommit(&batch, ago(4*h))

	dark := demoPR("web", 91, "feat(settings): add a dark mode toggle", DemoViewer, ago(28*h), "f0e1d2c3b4a5968778695a4b3c2d1e0f9a8b7c6d", "SUCCESS", 156, 22, 7)
	demoReview(&dark, "alice", "APPROVED", ago(6*h), "f0e1d2c3", "Looks good. Nice work on the contrast checks.")

	return &GitHub{
		Viewer: DemoViewer,
		PRs:    []gh.PR{auth, cart, logs, limits, batch, dark},
		Bodies: map[int]string{
			142: "Refresh tokens are now single use. Each refresh issues a new token in the same family and marks the old one used, so a stolen token stops working once its owner refreshes.\n\nThe migration adds `family_id` and `used_at` to `refresh_tokens`.",
			88:  "A session that expires mid-checkout used to empty the cart. The cart now lives in local storage until the next login and merges back into the server cart.",
			57:  "`nimbus logs --follow` streams new lines as they arrive, like `tail -f`. Ctrl-C stops it.",
			144: "Adds the per-plan limits and the `Retry-After` behavior to the API docs.",
			139: "Session lookups on the dashboard ran one query per widget. They now batch into a single `ANY($1)` query.",
			91:  "Adds a theme toggle to settings: light, dark, or follow the system.",
		},
		Comments: map[int][]gh.Comment{
			142: {{ID: "c1", Author: &gh.Actor{Login: "bob"}, CreatedAt: ago(5 * h),
				Body: "Should a reused token revoke the whole family, or only fail that request? The OAuth BCP says the family."}},
		},
		Threads: map[int][]gh.Thread{
			142: {demoThread("t1", "internal/auth/refresh.go", 37, ago(5*h), "bob", "Is `refreshTTL` the right lifetime for the new token, or should it keep the family's original expiry?")},
			88:  {demoThread("t2", "src/cart/useCart.ts", 24, ago(30*h), DemoViewer, "If the refresh fails here the cart is cleared before the error reaches the user.", "priya", "Fixed in the last push: the cart is saved before the refresh and only cleared after a successful login.")},
		},
		Checks: map[int][]any{
			142: {CheckRun("build", "SUCCESS", true, 0), CheckRun("test", "SUCCESS", true, 0), CheckRun("lint", "SUCCESS", false, 0)},
			57:  {CheckRun("build", "SUCCESS", true, 0), demoFailedTest(), CheckRun("lint", "SUCCESS", false, 0)},
			139: {CheckRun("build", "SUCCESS", true, 0), CheckRun("test", "", true, 0)},
		},
		JobLogs: map[int64]string{
			5701: "2026-11-11T10:00:01.0000000Z === RUN   TestFollow\n2026-11-11T10:00:01.0000000Z     follow_test.go:41: got 2 lines after cancel, want 0\n2026-11-11T10:00:01.0000000Z --- FAIL: TestFollow (0.21s)\n2026-11-11T10:00:01.0000000Z ##[error]Process completed with exit code 1.\n",
		},
		JobSteps: map[int64]string{5701: "Run go test ./..."},
		Diffs: map[int]string{
			142: demoAuthDiff,
			88:  demoCartDiff,
			57:  demoLogsDiff,
			144: newFileDiff("docs/rate-limits.md", "# Rate limits\n\n| Plan | Requests per minute |\n| --- | --- |\n| Free | 60 |\n| Team | 600 |\n| Enterprise | 6000 |\n\nOver the limit the API answers `429` with a `Retry-After` header in seconds.\n"),
			139: newFileDiff("internal/session/batch.go", "package session\n\n// LoadMany loads sessions by ID in one query.\nfunc (s *Store) LoadMany(ctx context.Context, ids []string) ([]Session, error) {\n\treturn s.query(ctx, `SELECT * FROM sessions WHERE id = ANY($1)`, ids)\n}\n"),
			91:  newFileDiff("src/settings/Theme.tsx", "export function Theme() {\n  const [theme, setTheme] = useTheme();\n  return <Select value={theme} onChange={setTheme} options={[\"light\", \"dark\", \"system\"]} />;\n}\n"),
		},
	}
}

// SeedDemo stores the finished AI review and summary of the demo's auth PR.
func SeedDemo(st *store.Store) error {
	id, err := st.StartRun(demoAuthKey, demoAuthOid, "claude", "", 0)
	if err != nil {
		return err
	}
	line := func(s string) int { return lineOf(demoRefresh, s) }
	findings := []store.Finding{
		{Severity: "must-fix", Path: "internal/auth/refresh.go", Line: line("return Pair{}, ErrTokenReused"),
			Title: "A reused token leaves the rest of its family valid",
			Body:  "A reused token is rejected, but whoever replayed it may hold the newer token from the same family. Revoke the family here before returning, so both stop working."},
		{Severity: "should-fix", Path: "internal/auth/refresh.go", Line: line("SET used_at = now() WHERE id = $1"),
			Title: "Two refreshes with the same token can both succeed",
			Body:  "The `used_at` check and this update are separate statements, so two requests racing with one token both pass. Add `AND used_at IS NULL` here and treat zero rows updated as reuse."},
		{Severity: "nit", Path: "internal/auth/refresh_test.go", Line: lineOf(demoRefreshTest, "func TestRotate"),
			Title: "No test for a reused token",
			Body:  "`TestRotate` covers the happy path only. A case that rotates twice with the same token would pin down the reuse behavior."},
	}
	summary := "**Verdict: request changes**\n\nRotation works and the migration is safe to run on a live table. A reused token only fails its own request, though, so a stolen token's family stays usable. That needs fixing before this ships."
	if err := st.FinishReview(id, "demo", summary, "", findings); err != nil {
		return err
	}
	if err := st.SetReviewModel(id, "opus"); err != nil {
		return err
	}
	if err := st.SavePoints(id, []store.Point{
		{Who: "bob", Point: "A reused token should revoke the whole family.", Verdict: "open",
			Why: "`Rotate` returns `ErrTokenReused` without touching the family. Raised as the must-fix finding."},
		{Who: "bob", Path: "internal/auth/refresh.go", Line: 37, Point: "Should the new token keep the family's original expiry?", Verdict: "unclear",
			Why: "The PR description doesn't say which lifetime is intended. Both are defensible."},
	}); err != nil {
		return err
	}
	if err := st.AddReviewLog(demoAuthKey, "claude", "agent", "I couldn't check how the mobile app handles a revoked family. If it retries the refresh in a loop, revoking could log people out repeatedly."); err != nil {
		return err
	}
	return st.SaveSummary(store.Summary{Key: demoAuthKey, HeadOid: demoAuthOid, Model: "haiku", At: time.Now(),
		Text: "Makes refresh tokens single use: each refresh marks the old token used and issues a new one in the same family.\n\n- `internal/auth/refresh.go`: the new `Rotate`, which rejects a token used before.\n- `migrations/0042_refresh_token_family.sql`: adds `family_id` and `used_at`.\n- `internal/auth/refresh_test.go`: tests the happy path.\n\nLook carefully at what happens to the family when a token is reused."})
}

func demoPR(repo string, n int, title, author string, created time.Time, oid, ci string, add, del, files int) gh.PR {
	p := gh.PR{ID: fmt.Sprintf("nimbus-%s-%d", repo, n), Number: n, Title: title, State: "OPEN",
		Author: &gh.Actor{Login: author}, CreatedAt: created, UpdatedAt: created,
		URL: fmt.Sprintf("https://github.com/nimbus/%s/pull/%d", repo, n), HeadRefOid: oid, BaseRefName: "main",
		HeadRefName: strings.NewReplacer("(", "/", ")", "").Replace(strings.SplitN(title, ":", 2)[0]), Additions: add, Deletions: del, ChangedFiles: files,
		Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "REVIEW_REQUIRED"}
	p.Repository.Name, p.Repository.NameWithOwner = repo, "nimbus/"+repo
	_ = json.Unmarshal(fmt.Appendf(nil, `{"commits":{"nodes":[{"commit":{"oid":%q,"statusCheckRollup":{"state":%q}}}]}}`, oid, ci), &p)
	return p
}

func demoRequest(p *gh.PR, who string, at time.Time) {
	*p = Requested(*p, who)
	demoTimeline(p, map[string]any{"__typename": "ReviewRequestedEvent", "createdAt": at, "requestedReviewer": map[string]any{"login": who}})
}

func demoCommit(p *gh.PR, at time.Time) {
	demoTimeline(p, map[string]any{"__typename": "PullRequestCommit", "commit": map[string]any{"oid": p.HeadRefOid, "committedDate": at}})
	p.UpdatedAt = at
}

func demoTimeline(p *gh.PR, item map[string]any) {
	var ti gh.TimelineItem
	b, _ := json.Marshal(item)
	_ = json.Unmarshal(b, &ti)
	p.TimelineItems.Nodes = append(p.TimelineItems.Nodes, ti)
}

func demoReview(p *gh.PR, who, state string, at time.Time, oid, body string) {
	var r gh.Review
	b, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("%s-%s-%d", p.ID, who, len(p.Reviews.Nodes)), "author": map[string]any{"login": who},
		"state": state, "submittedAt": at, "body": body, "commit": map[string]any{"oid": oid}})
	_ = json.Unmarshal(b, &r)
	p.Reviews.Nodes = append(p.Reviews.Nodes, r)
	switch state {
	case "APPROVED":
		p.ReviewDecision = "APPROVED"
	case "CHANGES_REQUESTED":
		p.ReviewDecision = "CHANGES_REQUESTED"
	}
	if at.After(p.UpdatedAt) {
		p.UpdatedAt = at
	}
}

// demoThread is a review thread: who said what, in turns.
func demoThread(id, path string, line int, at time.Time, turns ...string) gh.Thread {
	t := gh.Thread{ID: id, Path: path, Line: line, DiffSide: "RIGHT", ViewerCanResolve: true}
	for i := 0; i+1 < len(turns); i += 2 {
		t.Comments.Nodes = append(t.Comments.Nodes, gh.Comment{ID: fmt.Sprintf("%s-%d", id, i), Author: &gh.Actor{Login: turns[i]},
			CreatedAt: at.Add(time.Duration(i) * 13 * time.Hour), Body: turns[i+1], Path: path, Line: line})
	}
	b, _ := json.Marshal(map[string]any{"nodes": []any{map[string]any{"author": map[string]any{"login": turns[0]}}}})
	_ = json.Unmarshal(b, &t.Opener)
	return t
}

func demoFailedTest() map[string]any {
	c := CheckRun("test", "FAILURE", true, 5701)
	c["title"] = "1 test failed"
	c["annotations"] = map[string]any{"nodes": []any{map[string]any{"path": "cmd/logs/follow_test.go", "message": "got 2 lines after cancel, want 0",
		"annotationLevel": "FAILURE", "location": map[string]any{"start": map[string]any{"line": 41}}}}}
	return c
}

// newFileDiff is the unified diff that adds a file with content.
func newFileDiff(path, content string) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", path, path, path, len(lines))
	for _, l := range lines {
		fmt.Fprintf(&b, "+%s\n", l)
	}
	return b.String()
}

// editDiff is the unified diff of an edit to a file, as one hunk.
func editDiff(path, edit string) string {
	lines := strings.Split(strings.TrimSuffix(edit, "\n"), "\n")
	before, after := 0, 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "+") {
			before++
		}
		if !strings.HasPrefix(l, "-") {
			after++
		}
	}
	return fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,%d +1,%d @@\n%s", path, path, path, path, before, after, edit)
}

// newSide is the file an edit leaves behind.
func newSide(edit string) string {
	var b strings.Builder
	for _, l := range strings.SplitAfter(edit, "\n") {
		if l != "" && !strings.HasPrefix(l, "-") {
			b.WriteString(l[1:])
		}
	}
	return b.String()
}

func lineOf(content, s string) int {
	for i, l := range strings.Split(content, "\n") {
		if strings.Contains(l, s) {
			return i + 1
		}
	}
	return 0
}

// demoRefreshEdit is refresh.go's change: each line marked kept ( ),
// removed (-) or added (+).
const demoRefreshEdit = ` package auth
 
 import (
 	"context"
 	"errors"
 	"fmt"
 	"time"
 
 	"github.com/jackc/pgx/v5"
 )
 
+// ErrTokenReused means a refresh token was presented after it was rotated.
+var ErrTokenReused = errors.New("refresh token reused")
+
-// Rotate issues a new access token for a refresh token.
+// Rotate swaps a refresh token for a new one in the same family.
 func (s *Service) Rotate(ctx context.Context, token string) (Pair, error) {
 	var t refreshToken
 	err := s.db.QueryRow(ctx,
-		` + "`SELECT id, user_id FROM refresh_tokens WHERE hash = $1`" + `,
-		hash(token)).Scan(&t.ID, &t.UserID)
+		` + "`SELECT id, family_id, user_id, used_at FROM refresh_tokens WHERE hash = $1`" + `,
+		hash(token)).Scan(&t.ID, &t.FamilyID, &t.UserID, &t.UsedAt)
 	if errors.Is(err, pgx.ErrNoRows) {
 		return Pair{}, ErrInvalidToken
 	}
 	if err != nil {
 		return Pair{}, fmt.Errorf("rotate: %w", err)
 	}
+	if t.UsedAt != nil {
+		return Pair{}, ErrTokenReused
+	}
+
+	next := newToken()
+	if _, err := s.db.Exec(ctx,
+		` + "`UPDATE refresh_tokens SET used_at = now() WHERE id = $1`" + `, t.ID); err != nil {
+		return Pair{}, fmt.Errorf("rotate: %w", err)
+	}
+	if _, err := s.db.Exec(ctx,
+		` + "`INSERT INTO refresh_tokens (hash, family_id, user_id, expires_at) VALUES ($1, $2, $3, $4)`" + `,
+		hash(next), t.FamilyID, t.UserID, time.Now().Add(s.refreshTTL)); err != nil {
+		return Pair{}, fmt.Errorf("rotate: %w", err)
+	}
 	access, err := s.issueAccess(t.UserID)
 	if err != nil {
 		return Pair{}, err
 	}
-	return Pair{Access: access, Refresh: token}, nil
+	return Pair{Access: access, Refresh: next}, nil
 }
`

var demoRefresh = newSide(demoRefreshEdit)

const demoRefreshTest = `package auth_test

import (
	"context"
	"testing"
)

func TestRotate(t *testing.T) {
	s := newService(t)
	pair := login(t, s, "sam@nimbus.dev")
	next, err := s.Rotate(context.Background(), pair.Refresh)
	if err != nil {
		t.Fatal(err)
	}
	if next.Refresh == pair.Refresh {
		t.Fatal("rotate returned the same refresh token")
	}
}
`

const demoMigration = `ALTER TABLE refresh_tokens ADD COLUMN family_id uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE refresh_tokens ADD COLUMN used_at timestamptz;
CREATE INDEX refresh_tokens_family ON refresh_tokens (family_id);
`

var demoAuthDiff = editDiff("internal/auth/refresh.go", demoRefreshEdit) +
	newFileDiff("internal/auth/refresh_test.go", demoRefreshTest) +
	newFileDiff("migrations/0042_refresh_token_family.sql", demoMigration)

const demoCartDiff = `diff --git a/src/cart/useCart.ts b/src/cart/useCart.ts
--- a/src/cart/useCart.ts
+++ b/src/cart/useCart.ts
@@ -18,11 +18,15 @@ export function useCart() {
   const session = useSession();
   const [items, setItems] = useState<CartItem[]>(loadSaved);

   async function refresh() {
-    setItems([]);
-    const fresh = await session.refresh();
-    setItems(await fetchCart(fresh));
+    saveLocal(items);
+    const fresh = await session.refresh().catch(() => null);
+    if (!fresh) {
+      return;
+    }
+    setItems(merge(loadSaved(), await fetchCart(fresh)));
+    clearSaved();
   }

   return { items, refresh };
 }
`

const demoLogsDiff = `diff --git a/cmd/logs/follow.go b/cmd/logs/follow.go
--- a/cmd/logs/follow.go
+++ b/cmd/logs/follow.go
@@ -12,6 +12,18 @@ func run(ctx context.Context, opts Options) error {
 	if !opts.Follow {
 		return printAll(ctx, opts)
 	}
+	lines := make(chan string)
+	go stream(ctx, opts.Service, lines)
+	for {
+		select {
+		case <-ctx.Done():
+			return nil
+		case l := <-lines:
+			fmt.Println(l)
+		}
+	}
 }
`
