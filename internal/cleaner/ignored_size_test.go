package cleaner

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIgnoredNestedRepositoryIsMeasuredButProtected(t *testing.T) {
	_, repo, s := fixture(t)
	mustGit(t, repo, "config", "core.excludesfile", "/dev/null")
	write(t, filepath.Join(repo, ".gitignore"), ".build/\n.testdata/\n")
	write(t, filepath.Join(repo, ".testdata", "fixture.bin"), strings.Repeat("x", 8192))
	write(t, filepath.Join(repo, ".build", "clone", ".git", "objects", "large-pack"), strings.Repeat("x", 2<<20))
	scan := scanFixture(t, s, repo, 0)
	var build, data Item
	for _, item := range scan.Items {
		if item.Path == filepath.Join(repo, ".build") {
			build = item
		}
		if item.Path == filepath.Join(repo, ".testdata") {
			data = item
		}
	}
	if data.Bytes < 8192 || data.Fingerprint == "" {
		t.Fatal("unknown hidden ignored data was not discovered and measured")
	}
	if build.Bytes < 2<<20 || build.Fingerprint == "" {
		t.Fatalf("nested Git bytes must be measured completely: %+v", build)
	}
	if !strings.Contains(build.Blocked, "nested repository") {
		t.Fatal("measurement removed deletion protection")
	}
	p := plan(t, scan, PlanOptions{Safety: "unsafe"})
	for _, item := range p.Items {
		if item.Path == build.Path {
			t.Fatal("protected nested repository entered cleanup plan")
		}
	}
	cached := scanFixture(t, s, repo, time.Hour)
	for _, item := range cached.Items {
		if item.Path == build.Path && (!item.Cached || item.Blocked == "" || item.Bytes != build.Bytes) {
			t.Fatalf("cache lost nested-repository protection or size: %+v", item)
		}
	}
}
