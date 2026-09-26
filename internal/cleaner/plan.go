package cleaner

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

type PlanOptions struct {
	OlderThan time.Duration
	MinBytes  int64
	Kind      string
	Safety    string
	Limit     int
	Paths     []string
}

func MakePlan(scan Scan, opt PlanOptions) (Plan, error) {
	if opt.Safety == "" {
		opt.Safety = "review"
	}
	now := time.Now().UTC()
	p := Plan{ID: NewID(), ScanID: scan.ID, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), OlderThanSeconds: int64(opt.OlderThan.Seconds()), Items: []PlanItem{}, Notes: []string{
		"Allocated sizes are estimates; APFS clones and shared hard links can reduce actual reclaimed space.",
		"Cleanup permanently deletes selected items; it does not move them to Trash.",
		"Cached measurements can be stale. Apply remeasures and rejects changed items. Worktree age is newest filesystem modification, not proof that no agent is using it.",
	}}
	p.Safety = opt.Safety
	if scan.Partial {
		p.Notes = append(p.Notes, "This plan uses a partial scan. It covers only completed measurements, not every candidate on disk.")
	}
	if SafetyRank(opt.Safety) < 0 {
		return p, fmt.Errorf("safety must be safe, review, or unsafe")
	}
	if opt.OlderThan < 0 || opt.MinBytes < 0 || opt.Limit < 0 {
		return p, fmt.Errorf("age, minimum bytes, and limit cannot be negative")
	}
	if opt.Kind != "" && opt.Kind != "worktree" && opt.Kind != "ignored" && opt.Kind != "cache" {
		return p, fmt.Errorf("kind must be worktree, ignored, or cache")
	}
	requested := map[string]bool{}
	for _, path := range opt.Paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return p, err
		}
		requested[filepath.Clean(abs)] = false
	}
	// Prefer an eligible enclosing worktree. If it is protected, too recent,
	// or excluded with --kind ignored, its artifacts remain independent choices.
	eligible := []Item{}
	home, homeErr := canonicalHome()
	if homeErr != nil {
		return p, homeErr
	}
	for _, item := range scan.Items {
		item = deletionCandidate(item)
		if len(requested) > 0 {
			if _, ok := requested[item.Path]; !ok {
				continue
			}
		}
		if item.Kind == "storage" || managedStorageBoundary(item.Path, home) || item.RefreshRequired || item.Blocked != "" || item.Fingerprint == "" || item.Newest.After(now.Add(-opt.OlderThan)) || item.Bytes < opt.MinBytes {
			continue
		}
		if opt.Kind != "" && item.Kind != opt.Kind {
			continue
		}
		if SafetyRank(item.Risk) < 0 || SafetyRank(item.Risk) > SafetyRank(opt.Safety) {
			continue
		}
		eligible = append(eligible, item)
	}
	eligible = Outermost(eligible)
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].Bytes == eligible[j].Bytes {
			return eligible[i].Path < eligible[j].Path
		}
		return eligible[i].Bytes > eligible[j].Bytes
	})
	for _, item := range eligible {
		if opt.Limit > 0 && len(p.Items) >= opt.Limit {
			break
		}
		p.Items = append(p.Items, PlanItem{Item: item, Status: "pending"})
		p.EstimatedBytes += item.Bytes
		if item.Action == "delete" {
			p.DeletedBytes += item.Bytes
		} else {
			p.WorktreeBytes += item.Bytes
		}
		if len(requested) > 0 {
			requested[item.Path] = true
		}
	}
	for path, found := range requested {
		if !found {
			return p, fmt.Errorf("requested path is absent, blocked, or excluded by the plan filters: %s", path)
		}
	}
	return p, nil
}

type ApplyOptions struct{ Yes, AllowReview, AllowUnsafe bool }
type DeleteFunc func(string) error

func validateItem(ctx context.Context, p Plan, item Item) error {
	home, homeErr := canonicalHome()
	if homeErr != nil {
		return homeErr
	}
	if item.Kind == "storage" || managedStorageBoundary(item.Path, home) {
		return fmt.Errorf("managed storage cannot be deleted directly")
	}
	if item.Blocked != "" {
		return fmt.Errorf("protected item: %s", item.Blocked)
	}
	resolved, err := canonical(item.Path)
	if err != nil {
		return err
	}
	if resolved != item.Path {
		return fmt.Errorf("path now resolves through a symbolic link")
	}
	if item.Path == "/" || item.Path == home || item.Path == item.Repository {
		return fmt.Errorf("protected root directory")
	}
	id, err := identity(item.Path)
	if err != nil {
		return err
	}
	if id != item.Identity {
		return fmt.Errorf("item identity changed since scan")
	}
	fresh, err := measure(ctx, item)
	if err != nil {
		return err
	}
	if fresh.Blocked != "" {
		return fmt.Errorf("protected item: %s", fresh.Blocked)
	}
	if fresh.Fingerprint != item.Fingerprint {
		return fmt.Errorf("contents changed since measurement; run scan --refresh and create a new plan")
	}
	if fresh.Newest.After(time.Now().Add(-time.Duration(p.OlderThanSeconds) * time.Second)) {
		return fmt.Errorf("item no longer meets the plan age threshold")
	}
	switch item.Kind {
	case "ignored":
		if level, _ := classifyIgnored(item.Path); SafetyRank(level) > SafetyRank(item.Risk) {
			return fmt.Errorf("artifact safety changed since scan")
		}
		if item.Action != "delete" || item.Repository == "" || !Contains(item.Repository, item.Path) {
			return fmt.Errorf("invalid ignored-file action")
		}
		// Require the exact candidate to remain entirely untracked and ignored.
		// A parent directory can be reported even if a tracked file was added later,
		// so explicitly check the index underneath it before accepting the listing.
		rel, err := filepath.Rel(item.Repository, item.Path)
		if err != nil {
			return err
		}
		tracked, err := git(ctx, item.Repository, "ls-files", "-z", "--", ":(literal)"+rel)
		if err != nil {
			return err
		}
		if len(tracked) > 0 {
			return fmt.Errorf("item now contains tracked files")
		}
		paths, err := ignored(ctx, item.Repository)
		if err != nil {
			return err
		}
		found := false
		for _, path := range paths {
			if path == item.Path {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("path is no longer an ignored candidate")
		}
	case "worktree":
		if item.Action != "remove_worktree" {
			return fmt.Errorf("invalid worktree action")
		}
		trees, err := listWorktrees(ctx, item.Repository)
		if err != nil {
			return err
		}
		found := false
		for _, tree := range trees {
			if filepath.Clean(tree.Path) != item.Path {
				continue
			}
			found = true
			if tree.Branch != item.Branch {
				return fmt.Errorf("worktree branch changed")
			}
			blocked, err := worktreeBlock(ctx, tree)
			if err != nil {
				return err
			}
			if blocked != "" {
				return fmt.Errorf("worktree protected: %s", blocked)
			}
		}
		if !found {
			return fmt.Errorf("worktree is no longer registered")
		}
	case "cache":
		if item.Action != "delete" {
			return fmt.Errorf("invalid cache action")
		}
		rule, allowed := cacheRule(item.Path, home)
		if !allowed || item.Risk != rule.Safety {
			return fmt.Errorf("not a recognized cache location")
		}
	default:
		return fmt.Errorf("unknown item kind")
	}
	return nil
}

// Apply revalidates and journals each mutation. A nil remove uses the built-in
// cancellable, progress-reporting deleter; non-nil functions are test seams.
func Apply(ctx context.Context, s *Store, p Plan, opt ApplyOptions, remove DeleteFunc) (Plan, error) {
	if p.Invalidated {
		return p, fmt.Errorf("plan invalidated by inventory reset; create a new plan")
	}
	if !opt.Yes {
		return p, fmt.Errorf("no changes made; inspect the saved plan, then supply --yes to apply it")
	}
	if time.Now().After(p.ExpiresAt) {
		return p, fmt.Errorf("plan expired; create a new plan")
	}
	for _, item := range p.Items {
		if item.Action == "trash" {
			return p, fmt.Errorf("plan uses obsolete Trash actions; create a new plan to review permanent deletion")
		}
		if item.Status == "running" {
			return p, fmt.Errorf("an earlier apply was interrupted at %s; inspect the filesystem and create a new plan", item.Path)
		}
		if item.Risk == "review" && item.Status != "done" && !opt.AllowReview && !opt.AllowUnsafe {
			return p, fmt.Errorf("plan contains review items; --allow-review is required after inspecting their contents")
		}
		if item.Risk == "unsafe" && item.Status != "done" && !opt.AllowUnsafe {
			return p, fmt.Errorf("plan contains unsafe local data; --allow-unsafe is required after inspecting its contents")
		}
	}
	failed := 0
	for i := range p.Items {
		item := &p.Items[i]
		if item.Status == "done" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return p, err
		}
		validation := itemProgress(ctx, "Validating cleanup items", i, len(p.Items), item.Item)
		validationErr := s.checkGeneration(item.Item)
		if validationErr == nil {
			validationErr = validateItem(validation, p, item.Item)
		}
		if err := validationErr; err != nil {
			item.Status = "failed"
			item.Error = err.Error()
			failed++
			if err := s.SavePlan(p); err != nil {
				return p, err
			}
			continue
		}
		item.Status = "running"
		item.Error = ""
		// Journal intent before mutating. A crash leaves 'running', never a silently
		// retried deletion against a potentially replaced directory.
		if err := s.SavePlan(p); err != nil {
			return p, err
		}
		var err error
		deletion := itemProgress(ctx, "Applying cleanup items", i, len(p.Items), item.Item)
		if item.Action == "delete" {
			if remove != nil {
				err = remove(item.Path)
			} else {
				err = deleteWithProgress(deletion, item.Item)
			}
		} else {
			itemProgress(ctx, "Git removing worktree", i, len(p.Items), item.Item)
			_, err = git(ctx, item.Repository, "worktree", "remove", "--", item.Path)
		}
		if err != nil {
			item.Error = err.Error()
			failed++
			// Cancellation or a command error may occur after partial mutation. Leave
			// running to require reconciliation instead of retrying automatically.
			item.Status = "running"
		} else {
			item.Status = "done"
		}
		if saveErr := s.SavePlan(p); saveErr != nil {
			return p, saveErr
		}
		ReportProgress(ctx, "Applying cleanup items", i+1, len(p.Items))
	}
	if failed > 0 {
		return p, fmt.Errorf("%d item(s) failed or require reconciliation; inspect item status and error", failed)
	}
	return p, nil
}
