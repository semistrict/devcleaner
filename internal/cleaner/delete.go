package cleaner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Deletion uses directory handles so a replaced path or symlink cannot redirect
// traversal outside the selected tree. Counts advance only after successful
// removal; allocated bytes are estimates, not observed free-space changes.
func deleteWithProgress(ctx context.Context, item Item) error {
	parent, err := os.OpenRoot(filepath.Dir(item.Path))
	if err != nil {
		return err
	}
	defer parent.Close()
	progress := fileProgress{ctx: ctx, state: Progress{Phase: "Applying cleanup items", Path: item.Path, Activity: "deleted"}}
	progress.report(true)
	defer progress.report(true)
	seen := map[Identity]bool{}
	var remove func(*os.Root, string, *Identity) error
	remove = func(root *os.Root, name string, expected *Identity) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("unsupported filesystem metadata")
		}
		id := Identity{uint64(st.Dev), uint64(st.Ino)}
		if expected != nil && id != *expected {
			return fmt.Errorf("directory identity changed before deletion")
		}
		if id.Device != item.Identity.Device {
			return fmt.Errorf("mounted filesystem appeared during deletion")
		}
		if info.IsDir() {
			child, err := root.OpenRoot(name)
			if err != nil {
				return err
			}
			defer child.Close()
			opened, err := child.Stat(".")
			if err != nil {
				return err
			}
			if !os.SameFile(info, opened) {
				return fmt.Errorf("directory changed during deletion")
			}
			dir, err := child.Open(".")
			if err != nil {
				return err
			}
			defer dir.Close()
			for {
				entries, readErr := dir.ReadDir(256)
				for _, entry := range entries {
					if err := remove(child, entry.Name(), nil); err != nil {
						return err
					}
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					return readErr
				}
			}
			// Refuse a replacement entry at the original name after traversal.
			current, err := root.Lstat(name)
			if err != nil {
				return err
			}
			if !os.SameFile(info, current) {
				return fmt.Errorf("directory changed during deletion")
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Remove unlinks symlinks themselves, never their targets.
		if err := root.Remove(name); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			progress.state.Files++
		}
		if info.Mode()&os.ModeSymlink == 0 && !seen[id] {
			progress.state.Bytes += st.Blocks * 512
			seen[id] = true
		}
		progress.report(false)
		return nil
	}
	return remove(parent, filepath.Base(item.Path), &item.Identity)
}
