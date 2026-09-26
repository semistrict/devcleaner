package cleaner

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RefreshPaths reads only explicitly selected known candidates, recognized tool locations, and their Git
// metadata. It never discovers or traverses unrelated repositories.
func RefreshPaths(ctx context.Context, s *Store, paths []string, workers int) (Scan, error) {
	start := time.Now()
	if err := ValidateWorkers(workers); err != nil {
		return Scan{}, err
	}
	if len(paths) == 0 {
		return Scan{}, fmt.Errorf("refresh requires at least one --path")
	}
	inventory, err := s.LoadInventory()
	if err != nil {
		return Scan{}, err
	}
	known := map[string]Item{}
	for _, item := range inventory.Items {
		known[item.Path] = item
	}
	selected := []Item{}
	seen := map[string]bool{}
	for _, path := range paths {
		path, err = filepath.Abs(path)
		if err != nil {
			return Scan{}, err
		}
		if parent, e := canonical(filepath.Dir(path)); e == nil {
			path = filepath.Join(parent, filepath.Base(path))
		}
		item, ok := known[path]
		if !ok {
			// Refreshing a known deleted path observes a new generation if recreated.
			var data []byte
			if err := s.db.QueryRow("SELECT data FROM inventory WHERE path=? ORDER BY rowid DESC LIMIT 1", path).Scan(&data); err == nil {
				if err := json.Unmarshal(data, &item); err != nil {
					return Scan{}, err
				}
			} else if err != sql.ErrNoRows {
				return Scan{}, err
			} else {
				home, err := canonicalHome()
				if err != nil {
					return Scan{}, err
				}
				item, ok = cacheCandidate(path, home)
				if !ok {
					item, ok = storageCandidate(path, home)
				}
				if !ok {
					return Scan{}, fmt.Errorf("path is not in the saved inventory or recognized tool locations: %s; use scan --root to discover it", path)
				}
			}
		}
		home, err := canonicalHome()
		if err != nil {
			return Scan{}, err
		}
		item = protectManagedStorage(item, home)
		if !seen[path] {
			selected = append(selected, item)
			seen[path] = true
		}
	}
	type result struct {
		item    Item
		missing bool
		err     error
	}
	results := parallelProgress(ctx, workers, selected, "Refreshing selected paths", func(ctx context.Context, item Item) result {
		if err := ctx.Err(); err != nil {
			return result{err: err}
		}
		if _, err := os.Lstat(item.Path); os.IsNotExist(err) {
			return result{item: item, missing: true}
		} else if err != nil {
			return result{item: item, err: err}
		}
		item.RefreshRequired = false
		item.Blocked = ""
		item.Fingerprint = ""
		resolved, err := canonical(item.Path)
		if err != nil {
			return result{item: item, err: err}
		}
		if resolved != item.Path {
			return result{item: item, err: fmt.Errorf("path now resolves through a symbolic link")}
		}
		switch item.Kind {
		case "ignored":
			item.Risk, item.Tool = classifyIgnored(item.Path)
			paths, err := ignored(ctx, item.Repository)
			if err != nil {
				return result{item: item, err: err}
			}
			found := false
			for _, p := range paths {
				if p == item.Path {
					found = true
					break
				}
			}
			if !found {
				return result{item: item, err: fmt.Errorf("path is no longer an ignored candidate")}
			}
		case "worktree":
			trees, err := listWorktrees(ctx, item.Repository)
			if err != nil {
				return result{item: item, err: err}
			}
			found := false
			for _, tree := range trees {
				if tree.Path == item.Path {
					found = true
					item.Branch = tree.Branch
					item.Blocked, err = worktreeBlock(ctx, tree)
					if err != nil {
						return result{item: item, err: err}
					}
					break
				}
			}
			if !found {
				return result{item: item, err: fmt.Errorf("worktree no longer registered")}
			}
			item.Risk = "review"
			if item.Blocked != "" {
				item.Risk = "unsafe"
			}
		case "storage":
			home, err := canonicalHome()
			if err != nil {
				return result{item: item, err: err}
			}
			classified, ok := storageCandidate(item.Path, home)
			if !ok {
				return result{item: item, err: fmt.Errorf("unrecognized managed storage location")}
			}
			classified.InventoryID = item.InventoryID
			item = classified
		case "cache":
			home, err := canonicalHome()
			if err != nil {
				return result{item: item, err: err}
			}
			rule, ok := cacheRule(item.Path, home)
			if !ok {
				return result{item: item, err: fmt.Errorf("unrecognized cache location")}
			}
			item.Risk, item.Tool = rule.Safety, rule.Tool
		default:
			return result{item: item, err: fmt.Errorf("unknown candidate kind")}
		}
		home, err := canonicalHome()
		if err != nil {
			return result{item: item, err: err}
		}
		item = protectManagedStorage(item, home)
		measured, err := measure(ctx, item)
		return result{item: measured, err: err}
	})
	scan := Scan{ID: NewID(), CreatedAt: start.UTC(), DurationMS: time.Since(start).Milliseconds(), Workers: workers, Roots: paths, Items: []Item{}, Partial: ctx.Err() != nil}
	if scan.Partial {
		scan.StopReason = ctx.Err().Error()
	}
	for _, r := range results {
		if r.item.Path == "" {
			continue
		}
		if r.err != nil {
			if ctx.Err() != nil {
				continue
			}
			r.item.RefreshRequired = true
			r.item.Blocked = r.err.Error()
			r.item.Fingerprint = ""
			scan.Warnings = append(scan.Warnings, r.item.Path+": "+r.err.Error())
		}
		if r.missing {
			scan.RemovedPaths = append(scan.RemovedPaths, r.item.Path)
		} else {
			scan.Items = append(scan.Items, r.item)
		}
	}
	return scan, s.SaveScan(scan)
}
