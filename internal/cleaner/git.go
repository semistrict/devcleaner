package cleaner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

func git(ctx context.Context, root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	base := []string{"--no-optional-locks", "-C", root, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 600 {
			msg = msg[:600]
		}
		return nil, fmt.Errorf("git in %s: %s: %w", root, msg, err)
	}
	return out, nil
}

type worktree struct {
	Path, Branch           string
	Main, Locked, Prunable bool
}

func parseWorktrees(data []byte) ([]worktree, error) {
	var trees []worktree
	for _, field := range bytes.Split(data, []byte{0}) {
		if !utf8.Valid(field) {
			return nil, fmt.Errorf("Git returned a non-UTF-8 worktree path")
		}
		value := string(field)
		if strings.HasPrefix(value, "worktree ") {
			trees = append(trees, worktree{Path: strings.TrimPrefix(value, "worktree "), Main: len(trees) == 0})
			continue
		}
		if len(trees) == 0 {
			continue
		}
		tree := &trees[len(trees)-1]
		switch {
		case strings.HasPrefix(value, "branch "):
			tree.Branch = strings.TrimPrefix(value, "branch refs/heads/")
		case value == "detached":
			tree.Branch = "(detached)"
		case strings.HasPrefix(value, "locked"):
			tree.Locked = true
		case strings.HasPrefix(value, "prunable"):
			tree.Prunable = true
		}
	}
	return trees, nil
}
func listWorktrees(ctx context.Context, root string) ([]worktree, error) {
	out, err := git(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(out)
}
func ignored(ctx context.Context, root string) ([]string, error) {
	out, err := git(ctx, root, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		if !utf8.Valid(name) {
			return nil, fmt.Errorf("non-UTF-8 ignored filename; scan cannot safely represent it")
		}
		path := filepath.Clean(filepath.Join(root, string(name)))
		if path == root || !Contains(root, path) {
			return nil, fmt.Errorf("Git returned a path outside the repository")
		}
		protected := false
		for _, part := range strings.Split(string(name), "/") {
			if part == ".git" {
				protected = true
			}
		}
		if !protected {
			paths = append(paths, path)
		}
	}
	return paths, nil
}
func worktreeBlock(ctx context.Context, t worktree) (string, error) {
	if t.Main {
		return "main working copy is protected", nil
	}
	if t.Locked {
		return "locked worktree", nil
	}
	if t.Prunable {
		return "stale worktree registration; review with git worktree prune", nil
	}
	if t.Branch == "(detached)" || t.Branch == "" {
		return "detached HEAD; create a branch to retain its commits", nil
	}
	if _, err := os.Lstat(filepath.Join(t.Path, ".gitmodules")); err == nil {
		return "worktree contains submodules", nil
	}
	out, err := git(ctx, t.Path, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return "", err
	}
	if len(out) > 0 {
		return "modified or untracked files", nil
	}
	flags, err := git(ctx, t.Path, "ls-files", "-v", "-z")
	if err != nil {
		return "", err
	}
	for _, entry := range bytes.Split(flags, []byte{0}) {
		if len(entry) > 0 && (entry[0] == 'S' || (entry[0] >= 'a' && entry[0] <= 'z')) {
			return "index contains assume-unchanged or skip-worktree entries; Git may hide local changes", nil
		}
	}
	paths, err := ignored(ctx, t.Path)
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		if ignoredRisk(path) != "safe" {
			return "contains unrecognized ignored files or artifacts needing review; move local data before removing this worktree", nil
		}
	}
	return "", nil
}
