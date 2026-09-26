package cleaner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cancelAfterChecks struct {
	context.Context
	remaining int
	cancel    context.CancelFunc
}

func (c *cancelAfterChecks) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestInterruptedGroupRetainsCompletedArtifacts(t *testing.T) {
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"a", "b"} {
		for _, name := range []string{"one", "two", "three"} {
			write(t, filepath.Join(root, directory, name), "generated contents")
		}
	}
	items := []Item{{Path: root, Kind: "worktree"}, {Path: filepath.Join(root, "a"), Kind: "ignored"}, {Path: filepath.Join(root, "b"), Kind: "ignored"}}
	expected, err := measure(context.Background(), items[1])
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Root, a, three files under a, b, first file under b, then stop.
	ctx := &cancelAfterChecks{Context: base, remaining: 8, cancel: cancel}
	results := measureGroup(ctx, items)
	if !errors.Is(results[0].err, context.Canceled) || !errors.Is(results[2].err, context.Canceled) {
		t.Fatal("unfinished parent and sibling must remain incomplete")
	}
	completed := results[1]
	if completed.err != nil || completed.item.Fingerprint != expected.Fingerprint || completed.item.Bytes != expected.Bytes || completed.item.Files != expected.Files {
		t.Fatal("completed artifact was lost or its measurement changed")
	}
	if results[0].item.Fingerprint != "" || results[2].item.Fingerprint != "" {
		t.Fatal("unfinished measurements must not get usable fingerprints")
	}
}

func TestStopSavesCompletedGroupsAndPlan(t *testing.T) {
	root, repo, s := fixture(t)
	addTree(t, root, repo, "one")
	addTree(t, root, repo, "two")
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := WithProgress(base, func(p Progress) {
		if p.Phase == "Measuring directory trees" && p.Completed == 1 {
			cancel()
		}
	})
	partial, err := ScanDisk(ctx, s, ScanOptions{Roots: []string{repo}, Workers: 1, MaxDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !partial.Partial || len(partial.Items) != 3 {
		t.Fatalf("want one worktree and its two artifacts, got partial=%v items=%d", partial.Partial, len(partial.Items))
	}
	for _, item := range partial.Items {
		if item.Fingerprint == "" {
			t.Fatal("incomplete candidate leaked into partial scan")
		}
	}
	saved, err := s.LoadScan("")
	if err != nil || saved.ID != partial.ID || !saved.Partial {
		t.Fatal("partial scan was not saved")
	}
	p := plan(t, saved, PlanOptions{Kind: "ignored", Safety: "safe", OlderThan: 7 * 24 * time.Hour})
	if len(p.Items) != 2 {
		t.Fatal("completed artifacts must be usable in a plan")
	}
	found := false
	for _, note := range p.Notes {
		if strings.Contains(note, "partial scan") {
			found = true
		}
	}
	if !found {
		t.Fatal("plan must disclose its incomplete coverage")
	}
	full := scanFixture(t, s, repo, time.Hour)
	if full.Partial || full.CacheHits != 3 || len(full.Items) != 6 {
		t.Fatal("next scan must reuse completed work and finish the remaining inventory")
	}
}

func TestStopKeepsCompletedCacheChecks(t *testing.T) {
	root, repo, s := fixture(t)
	addTree(t, root, repo, "one")
	full := scanFixture(t, s, repo, 0)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := WithProgress(base, func(p Progress) {
		if p.Phase == "Checking measurement cache" && p.Completed == 1 {
			cancel()
		}
	})
	partial, err := ScanDisk(ctx, s, ScanOptions{Roots: []string{repo}, Workers: 1, MaxDepth: 8, CacheTTL: time.Hour})
	if err != nil || !partial.Partial || partial.CacheHits == 0 {
		t.Fatal("completed cache checks must be retained")
	}
	for _, item := range partial.Items {
		if !item.Cached || item.Fingerprint == "" {
			t.Fatal("partial cache candidate was not complete")
		}
	}
	if _, err := s.LoadScan(full.ID); err != nil {
		t.Fatal("prior complete snapshot was lost")
	}
}

func TestIncompleteFingerprintCannotBePlanned(t *testing.T) {
	// A directory can be partially counted, but it is not an actionable result.
	p := plan(t, Scan{Partial: true, Items: []Item{{Path: filepath.Join(os.TempDir(), "unfinished"), Kind: "ignored", Risk: "safe", Bytes: 1024, Newest: time.Now().Add(-time.Hour)}}}, PlanOptions{Safety: "safe"})
	if len(p.Items) != 0 || p.EstimatedBytes != 0 {
		t.Fatal("plan used an incomplete size estimate")
	}
}
