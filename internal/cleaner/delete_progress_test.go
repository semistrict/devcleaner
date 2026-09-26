package cleaner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteProgressCountsRemovalsAndPreservesSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "artifact")
	if err := os.MkdirAll(filepath.Join(path, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := range 1025 {
		if err := os.WriteFile(filepath.Join(path, fmt.Sprint(i)), []byte("build output"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	external := filepath.Join(root, "keep")
	if err := os.WriteFile(external, []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(path, "nested", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(path, "0"), filepath.Join(path, "hardlink")); err != nil {
		t.Fatal(err)
	}
	item, err := measure(context.Background(), Item{Path: path, Kind: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	intermediate := false
	var last Progress
	ctx := WithProgress(context.Background(), func(p Progress) {
		if p.Files < last.Files || p.Bytes < last.Bytes {
			t.Fatal("progress went backwards")
		}
		if p.Files > 0 && p.Files < item.Files {
			if _, err := os.Stat(path); err != nil {
				t.Fatal("progress arrived only after deletion")
			}
			intermediate = true
		}
		last = p
	})
	if err := deleteWithProgress(ctx, item); err != nil {
		t.Fatal(err)
	}
	if !intermediate || last.Files != item.Files || last.Bytes != item.Bytes {
		t.Fatalf("incorrect progress: %+v; measured %+v", last, item)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("artifact remains: %v", err)
	}
	if b, err := os.ReadFile(external); err != nil || string(b) != "source" {
		t.Fatal("symlink target was changed")
	}
}

func TestDeleteProgressCancellationPreservesRemainingFiles(t *testing.T) {
	path := t.TempDir()
	for i := range 10 {
		if err := os.WriteFile(filepath.Join(path, fmt.Sprint(i)), []byte("output"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	item, err := measure(context.Background(), Item{Path: path, Kind: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithProgress(ctx, func(p Progress) {
		if p.Files > 0 {
			cancel()
		}
	})
	if err := deleteWithProgress(ctx, item); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	remaining, err := os.ReadDir(path)
	if err != nil || len(remaining) != 9 {
		t.Fatalf("expected 9 remaining files, got %d: %v", len(remaining), err)
	}
}

func TestDeleteProgressRejectsReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "artifact")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	item, err := measure(context.Background(), Item{Path: path, Kind: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := deleteWithProgress(context.Background(), item); err == nil {
		t.Fatal("replacement must be protected")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("replacement removed")
	}
}
