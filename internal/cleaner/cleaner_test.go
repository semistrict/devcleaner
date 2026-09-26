package cleaner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mustGit(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	out, err := git(context.Background(), root, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) (string, string, *Store) {
	t.Helper()
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "project")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "-b", "main")
	write(t, filepath.Join(repo, ".gitignore"), "node_modules/\ntarget/\n.env\nlocal-data/\n")
	write(t, filepath.Join(repo, "Cargo.toml"), "[package]\nname = \"fixture\"\nversion = \"0.1.0\"\n")
	write(t, filepath.Join(repo, "source.txt"), "keep this source\n")
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-m", "Initial fixture")
	s, err := Open(filepath.Join(root, "state", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return root, repo, s
}
func addTree(t *testing.T, root, repo, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	mustGit(t, repo, "worktree", "add", "-b", name, path)
	write(t, filepath.Join(path, "node_modules", "dependency", "index.js"), strings.Repeat("a", 8192))
	write(t, filepath.Join(path, "target", "debug", "app"), strings.Repeat("b", 8192))
	age(t, path, 40*24*time.Hour)
	return path
}
func age(t *testing.T, path string, delta time.Duration) {
	t.Helper()
	at := time.Now().Add(-delta)
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return os.Chtimes(p, at, at)
	})
	if err != nil {
		t.Fatal(err)
	}
}
func scanFixture(t *testing.T, s *Store, repo string, ttl time.Duration) Scan {
	t.Helper()
	scan, err := ScanDisk(context.Background(), s, ScanOptions{Roots: []string{repo}, Workers: 4, CacheTTL: ttl, MaxDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", scan.Warnings)
	}
	return scan
}
func itemAt(t *testing.T, scan Scan, path string) Item {
	t.Helper()
	for _, i := range scan.Items {
		if i.Path == path {
			return i
		}
	}
	t.Fatalf("candidate missing: %s", path)
	return Item{}
}
func plan(t *testing.T, scan Scan, opt PlanOptions) Plan {
	t.Helper()
	p, err := MakePlan(scan, opt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWorktreeAndArtifactPlans(t *testing.T) {
	root, repo, s := fixture(t)
	clean := addTree(t, root, repo, "clean")
	dirty := addTree(t, root, repo, "dirty")
	write(t, filepath.Join(dirty, "source.txt"), "uncommitted work stays here")
	scan := scanFixture(t, s, repo, 0)
	if scan.Repositories != 3 {
		t.Fatalf("want main and two linked working copies, got %d", scan.Repositories)
	}
	if itemAt(t, scan, dirty).Blocked == "" {
		t.Fatal("dirty worktree must be protected")
	}
	generated := plan(t, scan, PlanOptions{Kind: "ignored", OlderThan: 30 * 24 * time.Hour})
	if len(generated.Items) != 4 {
		t.Fatalf("want artifacts in both worktrees, got %d", len(generated.Items))
	}
	for _, i := range generated.Items {
		if i.Kind != "ignored" || i.Risk != "safe" {
			t.Fatalf("wrong artifact plan: %+v", i)
		}
	}
	trees := plan(t, scan, PlanOptions{Kind: "worktree", OlderThan: 30 * 24 * time.Hour})
	if len(trees.Items) != 1 || trees.Items[0].Path != clean {
		t.Fatalf("only clean old worktree should be proposed: %+v", trees.Items)
	}
	mixed := plan(t, scan, PlanOptions{OlderThan: 30 * 24 * time.Hour})
	if len(mixed.Items) != 3 {
		t.Fatalf("want clean worktree plus dirty worktree's two artifacts, got %+v", mixed.Items)
	}
	var sum int64
	for _, a := range mixed.Items {
		sum += a.Bytes
		for _, b := range mixed.Items {
			if a.Path != b.Path && Contains(a.Path, b.Path) {
				t.Fatal("overlapping plan")
			}
		}
	}
	if sum != mixed.EstimatedBytes {
		t.Fatal("incorrect plan total")
	}
}

func TestGroupedMeasurementMatchesStandalone(t *testing.T) {
	root, repo, s := fixture(t)
	tree := addTree(t, root, repo, "linked")
	scan := scanFixture(t, s, repo, 0)
	for _, path := range []string{tree, filepath.Join(tree, "target"), filepath.Join(tree, "node_modules")} {
		grouped := itemAt(t, scan, path)
		single, err := measure(context.Background(), grouped)
		if err != nil {
			t.Fatal(err)
		}
		if grouped.Fingerprint != single.Fingerprint || grouped.Bytes != single.Bytes || grouped.Files != single.Files || !grouped.Newest.Equal(single.Newest) {
			t.Fatalf("grouped traversal differs for %s", path)
		}
	}
}

func TestCachedScanAndStaleApply(t *testing.T) {
	root, repo, s := fixture(t)
	tree := addTree(t, root, repo, "linked")
	first := scanFixture(t, s, repo, 0)
	target := filepath.Join(tree, "node_modules")
	before := itemAt(t, first, target)
	write(t, filepath.Join(target, "dependency", "new.js"), "new contents")
	cached := scanFixture(t, s, repo, time.Hour)
	after := itemAt(t, cached, target)
	if !after.Cached || after.Fingerprint != before.Fingerprint || cached.CacheHits == 0 {
		t.Fatal("measurement was not reused")
	}
	p := plan(t, cached, PlanOptions{Kind: "ignored", Paths: []string{target}})
	called := false
	applied, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true}, func(string) error { called = true; return nil })
	if err == nil || called || applied.Items[0].Status != "failed" {
		t.Fatal("changed cached data must never be deleted")
	}
	fresh := scanFixture(t, s, repo, 0)
	if itemAt(t, fresh, target).Fingerprint == before.Fingerprint || fresh.CacheHits != 0 {
		t.Fatal("refresh did not remeasure")
	}
}

func TestApplyArtifactsPreservesDirtyWorktreeAndIsIdempotent(t *testing.T) {
	root, repo, s := fixture(t)
	tree := addTree(t, root, repo, "dirty")
	write(t, filepath.Join(tree, "source.txt"), "valuable changes")
	scan := scanFixture(t, s, repo, 0)
	p := plan(t, scan, PlanOptions{Kind: "ignored", OlderThan: 30 * 24 * time.Hour})
	calls := 0
	remove := func(path string) error {
		calls++
		return os.RemoveAll(path)
	}
	if _, err := Apply(context.Background(), s, p, ApplyOptions{}, remove); err == nil || calls != 0 {
		t.Fatal("apply without --yes mutated data")
	}
	applied, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true}, remove)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 permanent deletions, got %d", calls)
	}
	content, err := os.ReadFile(filepath.Join(tree, "source.txt"))
	if err != nil || string(content) != "valuable changes" {
		t.Fatal("source was not preserved")
	}
	reloaded, err := s.LoadPlan(applied.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), s, reloaded, ApplyOptions{Yes: true}, remove); err != nil || calls != 2 {
		t.Fatal("reapply must not repeat completed actions")
	}
	for _, item := range reloaded.Items {
		if item.Status != "done" || item.Action != "delete" || item.TrashPath != "" {
			t.Fatal("missing durable cleanup journal")
		}
		if _, err := os.Lstat(item.Path); !os.IsNotExist(err) {
			t.Fatal("artifact was not deleted")
		}
	}
}

func TestRemoveCleanWorktreeRetainsBranch(t *testing.T) {
	root, repo, s := fixture(t)
	tree := addTree(t, root, repo, "unused")
	scan := scanFixture(t, s, repo, 0)
	p := plan(t, scan, PlanOptions{Kind: "worktree"})
	if _, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true}, nil); err == nil {
		t.Fatal("worktree removal must require review acknowledgement")
	}
	result, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true, AllowReview: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Status != "done" {
		t.Fatal("worktree not removed")
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Fatal("worktree still exists")
	}
	mustGit(t, repo, "show-ref", "--verify", "refs/heads/unused")
}

func TestWorktreeSafetyStates(t *testing.T) {
	root, repo, s := fixture(t)
	locked := addTree(t, root, repo, "locked")
	detached := addTree(t, root, repo, "detached")
	untracked := addTree(t, root, repo, "untracked")
	mustGit(t, repo, "worktree", "lock", locked)
	mustGit(t, detached, "checkout", "--detach")
	write(t, filepath.Join(untracked, "untracked.txt"), "uncommitted")
	scan := scanFixture(t, s, repo, 0)
	for _, path := range []string{locked, detached, untracked} {
		if itemAt(t, scan, path).Blocked == "" {
			t.Fatalf("unsafe worktree not blocked: %s", path)
		}
	}
	p := plan(t, scan, PlanOptions{Kind: "worktree"})
	if len(p.Items) != 0 {
		t.Fatal("protected worktree entered plan")
	}
}

func TestNewGitProtectionAfterPlan(t *testing.T) {
	for _, change := range []string{"lock", "tracked", "ignore-rule"} {
		t.Run(change, func(t *testing.T) {
			root, repo, s := fixture(t)
			tree := addTree(t, root, repo, "linked")
			target := filepath.Join(tree, "node_modules")
			kind := "ignored"
			if change == "lock" {
				kind = "worktree"
				target = tree
			}
			scan := scanFixture(t, s, repo, 0)
			p := plan(t, scan, PlanOptions{Kind: kind, Paths: []string{target}})
			switch change {
			case "lock":
				mustGit(t, repo, "worktree", "lock", tree)
			case "tracked":
				mustGit(t, tree, "add", "-f", "node_modules/dependency/index.js")
			case "ignore-rule":
				write(t, filepath.Join(tree, ".gitignore"), "target/\n")
			}
			called := false
			result, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true, AllowReview: true}, func(string) error { called = true; return nil })
			if err == nil || called || result.Items[0].Status != "failed" {
				t.Fatal("new Git protection did not block apply")
			}
		})
	}
}

func TestUnknownIgnoredFilesRequireExplicitReview(t *testing.T) {
	_, repo, s := fixture(t)
	write(t, filepath.Join(repo, ".env"), "LOCAL_SETTING=value")
	age(t, filepath.Join(repo, ".env"), 40*24*time.Hour)
	scan := scanFixture(t, s, repo, 0)
	p := plan(t, scan, PlanOptions{Kind: "ignored"})
	if len(p.Items) != 0 {
		t.Fatal("unknown ignored files included by default")
	}
	p = plan(t, scan, PlanOptions{Kind: "ignored", Safety: "unsafe"})
	if len(p.Items) != 1 || p.Items[0].Risk != "unsafe" {
		t.Fatal("explicit review candidate missing")
	}
	if _, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true}, nil); err == nil {
		t.Fatal("review acknowledgement missing")
	}
}

func TestSymlinksAndNestedRepositoriesProtected(t *testing.T) {
	root, repo, s := fixture(t)
	outside := filepath.Join(root, "outside")
	write(t, filepath.Join(outside, "precious.txt"), "do not touch")
	if err := os.Symlink(outside, filepath.Join(repo, "node_modules")); err != nil {
		t.Fatal(err)
	}
	// A trailing slash ignore pattern matches directories, not symlinks.
	write(t, filepath.Join(repo, ".gitignore"), "node_modules\nlocal-data/\n")
	nested := filepath.Join(repo, "local-data", "nested")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, nested, "init")
	scan := scanFixture(t, s, repo, 0)
	if itemAt(t, scan, filepath.Join(repo, "node_modules")).Blocked == "" {
		t.Fatal("symlink was not protected")
	}
	if itemAt(t, scan, filepath.Join(repo, "local-data")).Blocked == "" {
		t.Fatal("nested repository was not protected")
	}
	p := plan(t, scan, PlanOptions{Safety: "unsafe"})
	if len(p.Items) != 0 {
		t.Fatal("unsafe paths entered plan")
	}
}

func TestApplyRejectsReplacedDirectory(t *testing.T) {
	root, repo, s := fixture(t)
	tree := addTree(t, root, repo, "linked")
	target := filepath.Join(tree, "target")
	p := plan(t, scanFixture(t, s, repo, 0), PlanOptions{Paths: []string{target}})
	if err := os.Rename(target, target+"-original"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(target, "valuable"), "keep")
	called := false
	_, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true}, func(string) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("replacement directory must be preserved")
	}
}

func TestInterruptedMutationRequiresReconciliation(t *testing.T) {
	root, repo, s := fixture(t)
	tree := addTree(t, root, repo, "linked")
	p := plan(t, scanFixture(t, s, repo, 0), PlanOptions{Kind: "ignored", Paths: []string{filepath.Join(tree, "target")}})
	p, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true}, func(string) error { return errors.New("uncertain operation outcome") })
	if err == nil || p.Items[0].Status != "running" {
		t.Fatal("uncertain result must remain journaled")
	}
	called := false
	_, err = Apply(context.Background(), s, p, ApplyOptions{Yes: true}, func(string) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("uncertain mutation must not be repeated")
	}
}

func TestPersistenceLockAndExpiredPlan(t *testing.T) {
	_, repo, s := fixture(t)
	scan := scanFixture(t, s, repo, 0)
	loaded, err := s.LoadScan("")
	if err != nil || loaded.ID != scan.ID {
		t.Fatal("scan persistence failed")
	}
	other, err := Open(s.lock.Name()[:len(s.lock.Name())-5])
	if err == nil {
		other.Close()
		t.Fatal("concurrent invocations must not mutate shared state")
	}
	p := plan(t, scan, PlanOptions{})
	p.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true}, nil); err == nil {
		t.Fatal("expired plan accepted")
	}
}

func TestParallelWorkerBoundAndCancellation(t *testing.T) {
	var active, peak atomic.Int32
	input := make([]int, 40)
	for i := range input {
		input[i] = i
	}
	got := parallelMap(context.Background(), 4, input, func(ctx context.Context, n int) int {
		a := active.Add(1)
		defer active.Add(-1)
		for {
			p := peak.Load()
			if a <= p || peak.CompareAndSwap(p, a) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		return n * 2
	})
	if peak.Load() < 2 || peak.Load() > 4 {
		t.Fatalf("bad parallelism: %d", peak.Load())
	}
	for i, v := range got {
		if v != i*2 {
			t.Fatal("worker lost a result")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := measure(ctx, Item{Path: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
}

func TestParseWorktreePathsWithWhitespace(t *testing.T) {
	data := []byte("worktree /repo main\x00HEAD abc\x00branch refs/heads/main\x00\x00worktree /tree\nwith newline\x00HEAD def\x00branch refs/heads/feature\x00locked explanation\x00\x00")
	trees, err := parseWorktrees(data)
	if err != nil || len(trees) != 2 {
		t.Fatal("invalid parse")
	}
	if !trees[0].Main || trees[1].Main || trees[1].Path != "/tree\nwith newline" || !trees[1].Locked || trees[1].Branch != "feature" {
		t.Fatalf("wrong parse: %+v", trees)
	}
}

func TestIgnoredLocalDataProtectsWorktree(t *testing.T) {
	root, repo, s := fixture(t)
	tree := addTree(t, root, repo, "local-data")
	write(t, filepath.Join(tree, ".env"), "LOCAL_CONFIGURATION=valuable")
	scan := scanFixture(t, s, repo, 0)
	if !strings.Contains(itemAt(t, scan, tree).Blocked, "unrecognized ignored") {
		t.Fatal("ignored local data must protect worktree")
	}
	p := plan(t, scan, PlanOptions{Kind: "ignored"})
	if len(p.Items) != 2 {
		t.Fatal("generated artifacts should remain cleanable")
	}
}

func TestLocalOnlyCommittedBranchIsRemovable(t *testing.T) {
	root, repo, s := fixture(t)
	tree := addTree(t, root, repo, "local-only")
	write(t, filepath.Join(tree, "source.txt"), "committed only on this local branch")
	mustGit(t, tree, "add", "source.txt")
	mustGit(t, tree, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-m", "Local work")
	age(t, tree, 8*24*time.Hour)
	p := plan(t, scanFixture(t, s, repo, 0), PlanOptions{Kind: "worktree", OlderThan: 7 * 24 * time.Hour})
	if len(p.Items) != 1 {
		t.Fatal("local-only committed worktree must be eligible")
	}
	if _, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true, AllowReview: true}, nil); err != nil {
		t.Fatal(err)
	}
	content := mustGit(t, repo, "show", "local-only:source.txt")
	if string(content) != "committed only on this local branch" {
		t.Fatal("local committed work was not preserved")
	}
}

func TestLanguageRulesAndFlatSafetyLevels(t *testing.T) {
	cases := []struct{ name, marker, level string }{
		{"node_modules", "package.json", "safe"}, {"target", "Cargo.toml", "safe"}, {"target", "pom.xml", "safe"},
		{"target", "", "unsafe"}, {"build", "build.gradle.kts", "safe"}, {"build", "", "unsafe"},
		{"bin", "app.csproj", "safe"}, {"bin", "", "unsafe"}, {"obj", "app.fsproj", "safe"},
		{".build", "Package.swift", "safe"}, {".dart_tool", "pubspec.yaml", "safe"}, {"_build", "mix.exs", "safe"},
		{"zig-out", "build.zig", "safe"}, {"__pycache__", "", "safe"}, {".venv", "pyproject.toml", "review"},
		{"vendor/bundle", "Gemfile", "review"}, {"dist", "package.json", "review"},
	}
	for _, tt := range cases {
		t.Run(tt.name+"-"+tt.marker, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, tt.name)
			if err := os.MkdirAll(path, 0755); err != nil {
				t.Fatal(err)
			}
			if tt.marker != "" {
				write(t, filepath.Join(root, tt.marker), "marker")
			}
			if got := ignoredRisk(path); got != tt.level {
				t.Fatalf("want %s, got %s", tt.level, got)
			}
		})
	}
	now := time.Now().Add(-time.Hour)
	scan := Scan{Items: []Item{
		{Path: "/safe", Kind: "ignored", Risk: "safe", Fingerprint: "a", Newest: now, Bytes: 1},
		{Path: "/review", Kind: "worktree", Risk: "review", Fingerprint: "b", Newest: now, Bytes: 2},
		{Path: "/unsafe", Kind: "ignored", Risk: "unsafe", Fingerprint: "c", Newest: now, Bytes: 3},
		{Path: "/dirty", Kind: "worktree", Risk: "unsafe", Fingerprint: "d", Newest: now, Blocked: "modified files", Bytes: 4},
	}}
	for level, want := range map[string]int{"safe": 1, "review": 2, "unsafe": 3} {
		p := plan(t, scan, PlanOptions{Safety: level})
		if len(p.Items) != want {
			t.Fatalf("%s selected %d items, want %d", level, len(p.Items), want)
		}
	}
}

func TestDiscoveryAndCancellation(t *testing.T) {
	root, repo, s := fixture(t)
	tree := addTree(t, root, repo, "linked")
	// Scanning the containing root discovers both main and linked copies. The
	// owner must still be the main copy, never the worktree being removed.
	scan, err := ScanDisk(context.Background(), s, ScanOptions{Roots: []string{root, repo}, Workers: 2, MaxDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	if itemAt(t, scan, tree).Repository != repo {
		t.Fatal("worktree removal owner must be main repository")
	}
	p := plan(t, scan, PlanOptions{Kind: "worktree"})
	if err := validateItem(context.Background(), p, p.Items[0].Item); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	partial, err := ScanDisk(ctx, s, ScanOptions{Roots: []string{repo}, Workers: 2, MaxDepth: 8})
	if err != nil || !partial.Partial || len(partial.Items) != 0 {
		t.Fatal("stopping immediately must save an explicitly empty partial scan")
	}
	latest, err := s.LoadScan("")
	if err != nil || latest.ID != partial.ID {
		t.Fatal("partial snapshot is not the latest scan")
	}
	previous, err := s.LoadScan(scan.ID)
	if err != nil || previous.Partial {
		t.Fatal("previous complete scan must remain available by ID")
	}
}

func TestHiddenIndexChangesProtectWorktree(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			root, repo, s := fixture(t)
			tree := addTree(t, root, repo, "hidden")
			mustGit(t, tree, "update-index", flag, "source.txt")
			write(t, filepath.Join(tree, "source.txt"), "hidden local changes")
			scan := scanFixture(t, s, repo, 0)
			if itemAt(t, scan, tree).Blocked == "" {
				t.Fatal("hidden index changes must block removal")
			}
			p := plan(t, scan, PlanOptions{Kind: "worktree", Safety: "unsafe"})
			if len(p.Items) != 0 {
				t.Fatal("unsafe level must not override worktree protection")
			}
		})
	}

}
