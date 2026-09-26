package cleaner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func identity(path string) (Identity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Identity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Identity{}, fmt.Errorf("symbolic links are excluded: %s", path)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, fmt.Errorf("unsupported filesystem metadata")
	}
	return Identity{uint64(st.Dev), uint64(st.Ino)}, nil
}
func canonical(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

type measurement struct {
	item     Item
	hash     hash.Hash
	seen     map[Identity]bool
	err      error
	complete bool
}

const nestedRepositoryProtection = "contains a nested repository; protected"

// measureGroup walks an enclosing candidate once, accumulating independent
// fingerprints and sizes for it and its artifacts. An ignored dependency tree
// is never walked twice just to measure both it and its containing worktree.
func measureGroup(ctx context.Context, items []Item) []measurement {
	states := make([]measurement, len(items))
	byPath := map[string]int{}
	for i, item := range items {
		id, err := identity(item.Path)
		item.Identity = id
		item.Bytes = 0
		item.Files = 0
		item.Newest = time.Time{}
		item.Cached = false
		item.Fingerprint = ""
		states[i] = measurement{item: item, hash: sha256.New(), seen: map[Identity]bool{}, err: err}
		byPath[item.Path] = i
	}
	root := items[0].Path
	progress := fileProgress{ctx: ctx, state: Progress{Phase: "Measuring directory trees", Path: root, Activity: "measured"}}
	progress.report(true)
	defer progress.report(true)
	active := map[int]bool{}
	ancestors := func(path string, fn func(*measurement)) {
		for p := path; Contains(root, p); p = filepath.Dir(p) {
			if i, ok := byPath[p]; ok {
				fn(&states[i])
			}
			if p == root {
				break
			}
		}
	}
	if states[0].err == nil {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			// WalkDir visits each subtree contiguously. Once traversal has left a
			// candidate, its measurement is complete even if a later sibling is
			// interrupted. Keep these artifacts without claiming the parent is done.
			for index := range active {
				if !Contains(states[index].item.Path, path) {
					states[index].complete = true
					delete(active, index)
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if index, ok := byPath[path]; ok {
				active[index] = true
			}
			if walkErr != nil {
				ancestors(path, func(m *measurement) { m.err = walkErr })
				return nil
			}
			if path != root && d.Name() == ".git" {
				ownMetadata := false
				ancestors(path, func(m *measurement) {
					if filepath.Dir(path) != m.item.Path || m.item.Kind != "worktree" {
						m.item.Blocked = nestedRepositoryProtection
					} else {
						ownMetadata = true
					}
				})
				// Linked-worktree metadata belongs to Git, not the working copy.
				// Nested repositories still consume space inside an ignored folder:
				// measure them fully while retaining the deletion protection.
				if ownMetadata {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			info, err := d.Info()
			if err != nil {
				ancestors(path, func(m *measurement) { m.err = err })
				return nil
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return fmt.Errorf("unsupported filesystem metadata")
			}
			if uint64(st.Dev) != states[0].item.Identity.Device {
				ancestors(path, func(m *measurement) { m.err = fmt.Errorf("contains another mounted filesystem; protected") })
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			ancestors(path, func(m *measurement) {
				if m.err != nil {
					return
				}
				rel, _ := filepath.Rel(m.item.Path, path)
				fmt.Fprintf(m.hash, "%q %d %d %d %d %d\n", rel, info.Mode(), info.Size(), info.ModTime().UnixNano(), st.Dev, st.Ino)
				if info.ModTime().After(m.item.Newest) {
					m.item.Newest = info.ModTime()
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return
				}
				fid := Identity{uint64(st.Dev), uint64(st.Ino)}
				if !m.seen[fid] {
					m.item.Bytes += st.Blocks * 512
					m.seen[fid] = true
				}
				if info.Mode().IsRegular() {
					m.item.Files++
				}
			})
			progress.state.Files, progress.state.Bytes = states[0].item.Files, states[0].item.Bytes
			progress.report(false)
			return nil
		})
		if err != nil {
			for i := range states {
				if !states[i].complete {
					states[i].err = err
				}
			}
		}
	} else {
		for i := range states {
			states[i].err = states[0].err
		}
	}
	for i := range states {
		if states[i].err == nil {
			states[i].item.Fingerprint = hex.EncodeToString(states[i].hash.Sum(nil))
			states[i].item.MeasuredAt = time.Now().UTC()
		}
	}
	return states
}
func measure(ctx context.Context, item Item) (Item, error) {
	m := measureGroup(ctx, []Item{item})[0]
	return m.item, m.err
}

func cachedMeasurement(item Item, cache map[string]Item, ttl time.Duration) (Item, bool) {
	old, ok := cache[item.Path]
	id, err := identity(item.Path)
	if err == nil && ok && ttl > 0 && old.Kind == item.Kind && old.Identity == id && old.Fingerprint != "" && time.Since(old.MeasuredAt) >= 0 && time.Since(old.MeasuredAt) < ttl {
		item.Identity = id
		item.Bytes = old.Bytes
		item.Files = old.Files
		item.Newest = old.Newest
		item.MeasuredAt = old.MeasuredAt
		item.Fingerprint = old.Fingerprint
		item.Cached = true
		// Structural protection came from the walk, not the fresh Git inventory.
		// Keep it until a new measurement confirms the nested repository is gone.
		if old.Blocked == nestedRepositoryProtection {
			item.Blocked = old.Blocked
		}
		return item, true
	}
	return item, false
}
