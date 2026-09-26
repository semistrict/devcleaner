package cleaner

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestLegacyTrashPlanRequiresNewPermanentDeletionPlan(t *testing.T) {
	root, repo, s := fixture(t)
	addTree(t, root, repo, "generated")
	scan := scanFixture(t, s, repo, 0)
	legacy := plan(t, scan, PlanOptions{Kind: "ignored"})
	if len(legacy.Items) == 0 {
		t.Fatal("fixture has no artifacts")
	}
	for i := range legacy.Items {
		legacy.Items[i].Action = "trash"
	}
	if err := s.SavePlan(legacy); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadPlan(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = Apply(context.Background(), s, loaded, ApplyOptions{Yes: true, AllowUnsafe: true}, func(string) error { called = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "create a new plan") || called {
		t.Fatal("legacy plan must be rejected before any mutation")
	}
	for _, item := range loaded.Items {
		if _, err := os.Stat(item.Path); err != nil {
			t.Fatal("legacy plan changed an artifact", err)
		}
	}
	// Old inventory remains useful, but approval comes from a new explicit plan.
	for i := range scan.Items {
		if scan.Items[i].Kind == "ignored" {
			scan.Items[i].Action = "trash"
			scan.Items[i].Note = "Trash is recoverable until emptied; stop processes."
		}
	}
	scan.ID = NewID()
	if err := s.SaveScan(scan); err != nil {
		t.Fatal(err)
	}
	saved, err := s.LoadScan(scan.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range saved.Items {
		if item.Kind == "ignored" && (item.Action != "delete" || strings.Contains(item.Note, "recoverable")) {
			t.Fatal("old inventory still recommends Trash")
		}
	}
	replacement := plan(t, scan, PlanOptions{Kind: "ignored"})
	if replacement.ID == legacy.ID || replacement.DeletedBytes != replacement.EstimatedBytes || replacement.TrashBytes != 0 {
		t.Fatal("new plan must estimate direct deletion")
	}
	for _, item := range replacement.Items {
		if item.Action != "delete" {
			t.Fatal("new plan must explicitly propose deletion")
		}
	}
	result, err := Apply(context.Background(), s, replacement, ApplyOptions{Yes: true}, os.RemoveAll)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range result.Items {
		if item.Status != "done" {
			t.Fatal("deletion not journaled")
		}
		if _, err := os.Lstat(item.Path); !os.IsNotExist(err) {
			t.Fatal("artifact was not permanently deleted")
		}
	}
}
