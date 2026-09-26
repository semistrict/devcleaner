package command

import (
	"bytes"
	"context"
	"devcleaner/internal/cleaner"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusFlagsLargeUnknownAndProtectedIgnoredData(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.db")
	s, err := cleaner.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	scan := cleaner.Scan{ID: cleaner.NewID(), CreatedAt: time.Now(), Items: []cleaner.Item{
		{Path: "/unmounted/project/.testdata", Newest: time.Now().Add(10 * 365 * 24 * time.Hour), Kind: "ignored", Risk: "unsafe", Bytes: 70 << 30, Fingerprint: "complete"},
		{Path: "/unmounted/project/.build", Kind: "ignored", Risk: "unsafe", Bytes: 12 << 30, Fingerprint: "complete", Blocked: "contains a nested repository; protected"},
		{Path: "/unmounted/project/small", Kind: "ignored", Risk: "safe", Bytes: 1024, Fingerprint: "complete"},
	}}
	if err := s.SaveScan(scan); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var out bytes.Buffer
	if run(context.Background(), []string{"status", "--db", db}, &out, io.Discard) != 0 {
		t.Fatal(out.String())
	}
	for _, want := range []string{".testdata", ".build", "70.0 GiB", "12.0 GiB", "nested repository"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("large ignored finding %q hidden in status: %s", want, out.String())
		}
	}
	out.Reset()
	if run(context.Background(), []string{"list", "--kind", "ignored", "--min-bytes", "1073741824", "--db", db}, &out, io.Discard) != 0 {
		t.Fatal(out.String())
	}
	if !strings.Contains(out.String(), ".testdata") || !strings.Contains(out.String(), ".build") || strings.Contains(out.String(), "/small") {
		t.Fatal("size filter hid large unknown/protected data or included small data")
	}
}

func TestStatusAndListSurfaceProtectedManagedStorage(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.db")
	s, err := cleaner.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveScan(cleaner.Scan{ID: cleaner.NewID(), CreatedAt: time.Now(), Items: []cleaner.Item{{Path: "/offline/.lima/default", Kind: "storage", Risk: "unsafe", Tool: "Lima", Action: "none", Bytes: 140 << 30, Fingerprint: "complete", Blocked: "managed storage; direct deletion is disabled"}}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, args := range [][]string{{"status", "--db", db}, {"list", "--kind", "storage", "--db", db}} {
		var out bytes.Buffer
		if run(context.Background(), args, &out, io.Discard) != 0 {
			t.Fatal(out.String())
		}
		for _, want := range []string{"140.0 GiB", "/offline/.lima/default", "Lima"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("hidden storage %q: %s", want, out.String())
			}
		}
	}
}
