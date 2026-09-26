package cleaner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRefreshDiscoversNewToolLocationsWithoutRepositoryScan(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	paths := []string{}
	for _, rel := range []string{".cache/uv", "Library/Caches/lima", "Library/Caches/ms-playwright", ".lima/default", "Library/Containers/com.docker.docker/Data/vms", ".lnx/test-work"} {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "data"), []byte("allocated content"), 0600); err != nil {
			t.Fatal(err)
		}
		// lnx is reported at the store boundary, with guidance for its children.
		if rel == ".lnx/test-work" {
			path = filepath.Dir(path)
		}
		paths = append(paths, path)
	}
	scan, err := RefreshPaths(context.Background(), s, paths, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Items) != len(paths) {
		t.Fatalf("missing new locations: %+v", scan)
	}
	for _, item := range scan.Items {
		if item.Bytes == 0 || item.Fingerprint == "" {
			t.Fatalf("unmeasured item: %+v", item)
		}
		if item.Kind == "storage" && (item.Blocked == "" || item.Action != "none") {
			t.Fatalf("storage offered for deletion: %+v", item)
		}
	}
	plan, err := MakePlan(scan, PlanOptions{Safety: "unsafe"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 3 {
		t.Fatalf("want only the 3 caches, got %+v", plan.Items)
	}
	for _, item := range plan.Items {
		if item.Kind != "cache" {
			t.Fatal("storage in deletion plan")
		}
	}
	saved, err := s.LoadInventory()
	if err != nil || len(saved.Items) != len(paths) {
		t.Fatalf("inventory not saved: %v %+v", err, saved)
	}
	// A second refresh retains the managed-storage deletion prohibition.
	scan, err = RefreshPaths(context.Background(), s, paths[3:4], 1)
	if err != nil || len(scan.Items) != 1 || scan.Items[0].Blocked == "" {
		t.Fatalf("refresh lost protection: %v %+v", err, scan)
	}
}

func TestManagedStorageNeverEntersPlanEvenWithoutBlockReason(t *testing.T) {
	item := Item{Path: "/vm/disk", Kind: "storage", Risk: "unsafe", Action: "none", Bytes: 1 << 40, Fingerprint: "measured", Newest: time.Now().Add(-time.Hour)}
	plan, err := MakePlan(Scan{Items: []Item{item}}, PlanOptions{Safety: "unsafe"})
	if err != nil || len(plan.Items) != 0 {
		t.Fatalf("managed storage entered a deletion plan: %v %+v", err, plan)
	}
	if err := validateItem(context.Background(), plan, item); err == nil {
		t.Fatal("managed storage accepted by apply")
	}
}

func TestHomeCandidatesIncludeManagedStorageButNotConfiguration(t *testing.T) {
	home := t.TempDir()
	for _, rel := range []string{".lima/default", ".lima/_config", ".lnx-bench", "Library/Caches/bazel"} {
		if err := os.MkdirAll(filepath.Join(home, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	items := homeCandidates(home)
	if len(items) != 3 {
		t.Fatalf("unexpected discovery: %+v", items)
	}
	for _, item := range items {
		if filepath.Base(item.Path) == "_config" {
			t.Fatal("configuration counted as an instance")
		}
	}
}

func TestIgnoredManagedStorageCannotBypassProtection(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	mustGit(t, home, "init")
	if err := os.WriteFile(filepath.Join(home, ".gitignore"), []byte(".lima/\n.lnx/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".lima/default", ".lnx"} {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "disk"), []byte("persistent data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, caches := range []bool{false, true} {
		scan, err := ScanDisk(context.Background(), s, ScanOptions{Roots: []string{home}, Workers: 2, MaxDepth: 2, Caches: caches})
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, item := range scan.Items {
			if item.Path == filepath.Join(home, ".lima") || item.Path == filepath.Join(home, ".lnx") {
				found++
				if item.Blocked == "" {
					t.Fatalf("ignored storage is unprotected: %+v", item)
				}
				if item.Path == filepath.Join(home, ".lnx") && item.Kind != "storage" {
					t.Fatalf("dedup lost storage classification: %+v", item)
				}
			}
		}
		if found != 2 {
			t.Fatalf("missing ignored storage: %+v", scan.Items)
		}
		plan, err := MakePlan(scan, PlanOptions{Safety: "unsafe"})
		if err != nil || len(plan.Items) != 0 {
			t.Fatalf("storage enters plan: %v %+v", err, plan.Items)
		}
	}
	// Older snapshots/plans may label the exact path, its parent, or a child as
	// ignored. Current path rules must protect all three without scanning disk.
	for _, rel := range []string{".lima", ".lnx", ".lima/default/disk"} {
		old := Item{Path: filepath.Join(home, rel), Kind: "ignored", Risk: "unsafe", Action: "delete", Repository: home, Fingerprint: "old", Bytes: 1024}
		plan, err := MakePlan(Scan{Items: []Item{old}}, PlanOptions{Safety: "unsafe"})
		if err != nil || len(plan.Items) != 0 {
			t.Fatalf("stale classification bypass: %v %+v", err, plan.Items)
		}
		if err := validateItem(context.Background(), Plan{}, old); err == nil || !strings.Contains(err.Error(), "managed storage") {
			t.Fatalf("saved-plan boundary not enforced: %v", err)
		}
	}
	// Refresh must upgrade a pre-rule ignored classification in place.
	old := Item{Path: filepath.Join(home, ".lnx"), Kind: "ignored", Risk: "unsafe", Action: "delete", Repository: home}
	old, err = measure(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveScan(Scan{ID: NewID(), CreatedAt: time.Now(), Items: []Item{old}}); err != nil {
		t.Fatal(err)
	}
	before, err := s.LoadInventory()
	if err != nil {
		t.Fatal(err)
	}
	var generation string
	for _, item := range before.Items {
		if item.Path == old.Path {
			generation = item.InventoryID
		}
	}
	refreshed, err := RefreshPaths(context.Background(), s, []string{old.Path}, 1)
	if err != nil || len(refreshed.Items) != 1 || refreshed.Items[0].Kind != "storage" || refreshed.Items[0].Blocked == "" {
		t.Fatalf("refresh failed to reclassify: %v %+v", err, refreshed)
	}
	after, err := s.LoadInventory()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after.Items {
		if item.Path == old.Path && item.InventoryID != generation {
			t.Fatal("classification change incorrectly created a new generation")
		}
	}
}

func TestSymlinkedHomeRetainsStorageProtection(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", alias)
	mustGit(t, home, "init")
	if err := os.WriteFile(filepath.Join(home, ".gitignore"), []byte(".lnx/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".lnx")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "disk"), []byte("persistent data"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scan, err := ScanDisk(context.Background(), s, ScanOptions{Roots: []string{alias}, Workers: 1, MaxDepth: 2, Caches: true})
	if err != nil || len(scan.Items) != 1 || scan.Items[0].Kind != "storage" || scan.Items[0].Blocked == "" {
		t.Fatalf("symlinked home lost protection: %v %+v", err, scan)
	}
	old := scan.Items[0]
	old.Kind = "ignored"
	old.Action = "delete"
	old.Blocked = ""
	plan, err := MakePlan(Scan{Items: []Item{old}}, PlanOptions{Safety: "unsafe"})
	if err != nil || len(plan.Items) != 0 {
		t.Fatalf("symlinked home plan bypass: %v %+v", err, plan)
	}
	if err := validateItem(context.Background(), Plan{}, old); err == nil || !strings.Contains(err.Error(), "managed storage") {
		t.Fatalf("symlinked home apply bypass: %v", err)
	}
	refreshed, err := RefreshPaths(context.Background(), s, []string{filepath.Join(alias, ".lnx")}, 1)
	if err != nil || len(refreshed.Items) != 1 || refreshed.Items[0].Kind != "storage" {
		t.Fatalf("symlinked home refresh lost storage: %v %+v", err, refreshed)
	}
}
