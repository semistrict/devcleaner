package cleaner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ScanOptions struct {
	Roots    []string
	Workers  int
	CacheTTL time.Duration
	Caches   bool
	MaxDepth int
}
type discoveryJob struct {
	path  string
	depth int
}
type discoveryResult struct {
	repo     string
	children []discoveryJob
	warnings []string
}

func discover(ctx context.Context, roots []string, workers, depth int) ([]string, []string, error) {
	queue := []discoveryJob{}
	for _, p := range roots {
		queue = append(queue, discoveryJob{p, 0})
	}
	repos := []string{}
	warnings := []string{}
	seen := map[string]bool{}
	for len(queue) > 0 {
		results := parallelProgress(ctx, workers, queue, fmt.Sprintf("Discovering directories at depth %d", queue[0].depth), func(ctx context.Context, j discoveryJob) discoveryResult {
			result := discoveryResult{}
			if ctx.Err() != nil {
				return result
			}
			if _, err := os.Lstat(filepath.Join(j.path, ".git")); err == nil {
				result.repo = j.path
				return result
			}
			entries, err := os.ReadDir(j.path)
			if err != nil {
				result.warnings = append(result.warnings, fmt.Sprintf("%s: %v", j.path, err))
				return result
			}
			for _, entry := range entries {
				if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				name := entry.Name()
				switch name {
				case "node_modules", "target", ".build", ".git", "Library", ".Trash":
					continue
				}
				if strings.HasPrefix(name, ".") && name != ".worktrees" && name != ".claude" {
					continue
				}
				if j.depth >= depth {
					result.warnings = append(result.warnings, "discovery depth limit reached at "+j.path)
					break
				}
				result.children = append(result.children, discoveryJob{filepath.Join(j.path, name), j.depth + 1})
			}
			return result
		})
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		queue = nil
		for _, r := range results {
			warnings = append(warnings, r.warnings...)
			if r.repo != "" && !seen[r.repo] {
				seen[r.repo] = true
				repos = append(repos, r.repo)
			}
			queue = append(queue, r.children...)
		}
	}
	sort.Strings(repos)
	return repos, warnings, nil
}

type repoInventory struct {
	items    []Item
	trees    []worktree
	warnings []string
}

func inspectRepo(ctx context.Context, root string) repoInventory {
	r := repoInventory{}
	trees, err := listWorktrees(ctx, root)
	if err != nil {
		r.warnings = append(r.warnings, err.Error())
		return r
	}
	r.trees = trees
	paths, err := ignored(ctx, root)
	if err != nil {
		r.warnings = append(r.warnings, err.Error())
		return r
	}
	for _, p := range paths {
		level, tool := classifyIgnored(p)
		r.items = append(r.items, Item{Path: p, Kind: "ignored", Risk: level, Tool: tool, Action: "delete", Repository: root, Note: "Ignored by Git. Deletion is permanent; stop processes using this item before cleanup."})
	}
	return r
}

func ScanDisk(ctx context.Context, s *Store, opt ScanOptions) (scan Scan, resultErr error) {
	start := time.Now()
	scan = Scan{ID: NewID(), CreatedAt: start.UTC(), Workers: opt.Workers, Items: []Item{}, Warnings: []string{}}
	if err := ValidateWorkers(opt.Workers); err != nil {
		return scan, err
	}
	if len(opt.Roots) == 0 {
		return scan, fmt.Errorf("at least one scan root is required")
	}
	if opt.MaxDepth < 0 || opt.MaxDepth > 64 {
		return scan, fmt.Errorf("max-depth must be between 0 and 64")
	}
	if opt.CacheTTL < 0 {
		return scan, fmt.Errorf("cache-ttl cannot be negative")
	}
	ReportProgress(ctx, "Resolving scan roots", 0, len(opt.Roots))
	for _, root := range opt.Roots {
		p, err := canonical(root)
		if err != nil {
			return scan, err
		}
		info, err := os.Stat(p)
		if err != nil {
			return scan, err
		}
		if !info.IsDir() {
			return scan, fmt.Errorf("scan root is not a directory: %s", p)
		}
		redundant := false
		for _, r := range scan.Roots {
			if Contains(r, p) {
				redundant = true
			}
		}
		if !redundant {
			filtered := scan.Roots[:0]
			for _, r := range scan.Roots {
				if !Contains(p, r) {
					filtered = append(filtered, r)
				}
			}
			scan.Roots = append(filtered, p)
		}
	}
	// Cancellation is a request to finish with useful completed measurements.
	// Persistence deliberately does not use the cancelled context.
	defer func() {
		if resultErr != nil && ctx.Err() == nil {
			return
		}
		if ctx.Err() != nil {
			scan.Partial = true
			scan.StopReason = ctx.Err().Error()
			completed := []Item{}
			for _, item := range scan.Items {
				if item.Fingerprint != "" {
					completed = append(completed, item)
				}
			}
			scan.Items = completed
			scan.Warnings = append(scan.Warnings, "Scan stopped early. Only completed measurements are included; unvisited or unfinished candidates are excluded.")
		}
		scan.CacheHits = 0
		for _, item := range scan.Items {
			if item.Cached {
				scan.CacheHits++
			}
		}
		sort.Slice(scan.Items, func(i, j int) bool {
			if scan.Items[i].Bytes == scan.Items[j].Bytes {
				return scan.Items[i].Path < scan.Items[j].Path
			}
			return scan.Items[i].Bytes > scan.Items[j].Bytes
		})
		scan.DurationMS = time.Since(start).Milliseconds()
		ReportProgress(ctx, "Saving scan snapshot", 0, 0)
		resultErr = s.SaveScan(scan)
	}()
	ReportProgress(ctx, "Loading cached measurements", 0, 0)
	cache, err := s.Cache()
	if err != nil {
		return scan, err
	}
	repos, warnings, err := discover(ctx, scan.Roots, opt.Workers, opt.MaxDepth)
	if err != nil {
		return scan, err
	}
	scan.Warnings = append(scan.Warnings, warnings...)
	inventories := parallelProgress(ctx, opt.Workers, repos, "Inspecting repositories", inspectRepo)
	items := []Item{}
	trees := map[string]worktree{}
	owners := map[string]string{}
	for _, r := range inventories {
		scan.Warnings = append(scan.Warnings, r.warnings...)
		items = append(items, r.items...)
		for _, t := range r.trees {
			// Linked worktrees can live outside roots. Follow Git registration, but
			// never act through symbolic-link aliases or include the main worktree.
			if t.Main {
				continue
			}
			p, err := canonical(t.Path)
			if err != nil {
				scan.Warnings = append(scan.Warnings, "unavailable worktree: "+t.Path)
				continue
			}
			if p != filepath.Clean(t.Path) {
				scan.Warnings = append(scan.Warnings, "symlinked worktree excluded: "+t.Path)
				continue
			}
			trees[p] = t
			owners[p] = r.trees[0].Path
		}
	}
	// Registered linked worktrees are separate working copies, including those
	// outside the discovery roots. Inventory their ignored artifacts as well.
	discovered := map[string]bool{}
	for _, repo := range repos {
		discovered[repo] = true
	}
	additional := []string{}
	for path := range trees {
		if !discovered[path] {
			additional = append(additional, path)
		}
	}
	sort.Strings(additional)
	extra := parallelProgress(ctx, opt.Workers, additional, "Inspecting linked worktrees", inspectRepo)
	for _, r := range extra {
		items = append(items, r.items...)
		scan.Warnings = append(scan.Warnings, r.warnings...)
	}
	scan.Repositories = len(repos) + len(additional)
	treeList := make([]worktree, 0, len(trees))
	for _, t := range trees {
		treeList = append(treeList, t)
	}
	sort.Slice(treeList, func(i, j int) bool { return treeList[i].Path < treeList[j].Path })
	treeItems := parallelProgress(ctx, opt.Workers, treeList, "Checking worktree safety", func(ctx context.Context, t worktree) Item {
		blocked, err := worktreeBlock(ctx, t)
		if err != nil {
			blocked = err.Error()
		}
		level := "review"
		if blocked != "" {
			level = "unsafe"
		}
		return Item{Path: t.Path, Kind: "worktree", Risk: level, Tool: "Git", Action: "remove_worktree", Repository: owners[t.Path], Branch: t.Branch, Blocked: blocked, Note: "Git removes this directory permanently, including recognized generated artifacts. Its branch is retained. Confirm the worktree is no longer in use."}
	})
	items = append(items, treeItems...)
	if opt.Caches {
		home, err := canonicalHome()
		if err != nil {
			return scan, err
		}
		items = append(items, homeCandidates(home)...)
	}
	// Apply storage protection even when caches were not requested, and before
	// exact-path deduplication can discard a managed-storage representation.
	home, err := canonicalHome()
	if err != nil {
		return scan, err
	}
	for i := range items {
		items[i] = protectManagedStorage(items[i], home)
	}
	// Keep both a worktree and its artifacts. Plans choose a non-overlapping
	// set, allowing artifact-only cleanup even when the worktree is dirty.
	sort.Slice(items, func(i, j int) bool {
		if len(items[i].Path) == len(items[j].Path) {
			return items[i].Kind == "worktree" && items[j].Kind != "worktree"
		}
		return len(items[i].Path) < len(items[j].Path)
	})
	unique := []Item{}
	seenItems := map[string]bool{}
	for _, item := range items {
		if !seenItems[item.Path] {
			unique = append(unique, item)
			seenItems[item.Path] = true
		}
	}
	measured := []Item{}
	groups := [][]Item{}
	groupForPath := map[string]int{}
	ReportProgress(ctx, "Checking measurement cache", 0, len(unique))
	for index, item := range unique {
		if ctx.Err() != nil {
			break
		}
		ReportProgress(ctx, "Checking measurement cache", index, len(unique))
		if cached, ok := cachedMeasurement(item, cache, opt.CacheTTL); ok {
			measured = append(measured, cached)
			continue
		}
		group := -1
		for parent := filepath.Dir(item.Path); parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
			if index, ok := groupForPath[parent]; ok {
				group = index
				break
			}
		}
		if group < 0 {
			group = len(groups)
			groups = append(groups, []Item{})
		}
		groups[group] = append(groups[group], item)
		groupForPath[item.Path] = group
	}
	results := parallelProgress(ctx, opt.Workers, groups, "Measuring directory trees", measureGroup)
	for _, group := range results {
		for _, result := range group {
			if result.err != nil {
				result.item.Blocked = "measurement incomplete: " + result.err.Error()
				result.item.Risk = "unsafe"
				result.item.Fingerprint = ""
			}
			measured = append(measured, result.item)
		}
	}
	scan.Items = measured
	return scan, nil
}
