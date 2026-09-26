package cleaner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParallelProgressIsMonotonic(t *testing.T) {
	events := []Progress{}
	ctx := WithProgress(context.Background(), func(p Progress) { events = append(events, p) })
	input := make([]int, 40)
	parallelProgress(ctx, 8, input, "Measuring", func(context.Context, int) int { return 1 })
	if len(events) != 41 {
		t.Fatalf("expected start plus 40 completions, got %d", len(events))
	}
	for i, p := range events {
		if p.Completed != i || p.Total != 40 || p.Phase != "Measuring" {
			t.Fatalf("out of order progress: %+v", p)
		}
	}
}
func TestScanAndApplyReportPhases(t *testing.T) {
	root, repo, s := fixture(t)
	addTree(t, root, repo, "linked")
	phases := map[string]bool{}
	var mu sync.Mutex
	ctx := WithProgress(context.Background(), func(p Progress) { mu.Lock(); defer mu.Unlock(); phases[p.Phase] = true })
	scan, err := ScanDisk(ctx, s, ScanOptions{Roots: []string{repo}, Workers: 4, MaxDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"Discovering directories at depth 0", "Inspecting repositories", "Checking worktree safety", "Measuring directory trees", "Saving scan snapshot"} {
		if !phases[phase] {
			t.Fatalf("missing scan phase %s", phase)
		}
	}
	p := plan(t, scan, PlanOptions{Kind: "ignored", Limit: 1})
	// A failing injected deletion operation exercises progress without touching data.
	_, err = Apply(ctx, s, p, ApplyOptions{Yes: true}, func(string) error { return context.Canceled })
	if err == nil || !phases["Validating cleanup items"] || !phases["Applying cleanup items"] {
		t.Fatal("apply did not report validation and mutation phases")
	}
	for phase := range phases {
		if strings.Contains(phase, repo) {
			t.Fatal("progress should not expose raw filesystem paths")
		}
	}
}

func TestMeasurementReportsWithinOneDirectory(t *testing.T) {
	_, repo, _ := fixture(t)
	path := filepath.Join(repo, "node_modules")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if err := os.WriteFile(filepath.Join(path, fmt.Sprint(i)), []byte("artifact"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var events []Progress
	ctx := WithProgress(context.Background(), func(p Progress) { events = append(events, p) })
	item, err := measure(ctx, Item{Path: path, Kind: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	intermediate := false
	for _, p := range events {
		if p.Files > 0 && p.Files < item.Files {
			intermediate = true
		}
	}
	if !intermediate || len(events) == 0 {
		t.Fatal("single-folder measurement needs updates before completion")
	}
	last := events[len(events)-1]
	if last.Files != item.Files || last.Bytes != item.Bytes || last.Path != path || last.Activity != "measured" {
		t.Fatalf("incorrect final measurement progress: %+v", last)
	}
}
