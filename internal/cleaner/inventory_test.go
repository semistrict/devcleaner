package cleaner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInventorySurvivesEmptyPartialScanAndCleanup(t *testing.T) {
	root, repo, s := fixture(t)
	addTree(t, root, repo, "linked")
	scan := scanFixture(t, s, repo, 0)
	p := plan(t, scan, PlanOptions{Kind: "ignored", Limit: 1})
	if _, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveScan(Scan{ID: NewID(), CreatedAt: time.Now(), Partial: true}); err != nil {
		t.Fatal(err)
	}
	inv, err := s.LoadInventory()
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Items) == 0 {
		t.Fatal("empty partial scan discarded inventory")
	}
	for _, i := range inv.Items {
		if Contains(p.Items[0].Path, i.Path) {
			t.Fatal("deleted item is still offered")
		}
		if Contains(i.Path, p.Items[0].Path) && !i.RefreshRequired {
			t.Fatal("ancestor kept stale measurement")
		}
	}
	next := plan(t, inv, PlanOptions{Safety: "review"})
	for _, i := range next.Items {
		if i.Path == p.Items[0].Path || i.RefreshRequired {
			t.Fatal("stale item entered new plan")
		}
	}
	old, err := s.LoadScan(scan.ID)
	if err != nil || len(old.Items) != len(scan.Items) {
		t.Fatal("historical scan was changed")
	}
}

func TestInventoryMergesScopesAndPersists(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/one/node_modules", "/two/target"} {
		if err := s.SaveScan(Scan{ID: NewID(), CreatedAt: time.Now(), Items: []Item{{Path: p, Kind: "ignored", Risk: "safe", Fingerprint: "saved"}}}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	inv, err := s.LoadInventory()
	if err != nil || len(inv.Items) != 2 {
		t.Fatalf("inventory lost independent scopes: %+v %v", inv, err)
	}
}

func TestRecreatedPathGetsNewGenerationAndHistorySurvivesReset(t *testing.T) {
	_, repo, s := fixture(t)
	path := filepath.Join(repo, "node_modules")
	write(t, filepath.Join(path, "output"), "first output")
	scan := scanFixture(t, s, repo, 0)
	p := plan(t, scan, PlanOptions{Kind: "ignored"})
	firstID := p.Items[0].InventoryID
	firstBytes := p.Items[0].Bytes
	if _, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true}, nil); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, "output"), "second output")
	scan, err := RefreshPaths(context.Background(), s, []string{path}, 1)
	if err != nil {
		t.Fatal(err)
	}
	next := plan(t, scan, PlanOptions{Kind: "ignored"})
	if err := s.SavePlan(next); err != nil {
		t.Fatal(err)
	}
	if next.Items[0].InventoryID == firstID {
		t.Fatal("recreated path reused a deleted generation")
	}
	// Re-saving the old completed journal must not retire the new object.
	old, err := s.LoadPlan(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlan(old); err != nil {
		t.Fatal(err)
	}
	inv, err := s.LoadInventory()
	if err != nil || len(inv.Items) != 1 {
		t.Fatalf("new generation lost: %+v %v", inv, err)
	}
	h, err := s.History()
	if err != nil || h.Items != 1 || h.Bytes != firstBytes {
		t.Fatalf("wrong history: %+v %v", h, err)
	}
	if err := s.ResetInventory(); err != nil {
		t.Fatal(err)
	}
	inv, err = s.LoadInventory()
	if err != nil || len(inv.Items) != 0 {
		t.Fatal("reset kept active inventory")
	}
	after, err := s.History()
	if err != nil || after != h {
		t.Fatal("reset lost cleanup history")
	}
	retained, err := s.LoadPlan(next.ID)
	if err != nil || !retained.Invalidated {
		t.Fatal("reset did not invalidate pending plan")
	}
	if _, err := Apply(context.Background(), s, retained, ApplyOptions{Yes: true}, nil); err == nil {
		t.Fatal("reset plan was executable")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("reset touched disk files")
	}
	var state string
	var data []byte
	if err := s.db.QueryRow("SELECT state,data FROM inventory WHERE id=?", firstID).Scan(&state, &data); err != nil || state != "deleted" {
		t.Fatal("deleted metadata missing")
	}
	var saved Item
	if err := json.Unmarshal(data, &saved); err != nil || saved.Bytes != firstBytes || saved.Fingerprint == "" {
		t.Fatal("deleted metadata changed")
	}
}

func TestRefreshTouchesOnlySelectedPath(t *testing.T) {
	root, repo, s := fixture(t)
	tree := addTree(t, root, repo, "linked")
	scan := scanFixture(t, s, repo, 0)
	var target, other Item
	for _, i := range scan.Items {
		if i.Path == filepath.Join(tree, "target") {
			target = i
		}
		if i.Path == filepath.Join(tree, "node_modules") {
			other = i
		}
	}
	write(t, filepath.Join(target.Path, "new-output"), "new")
	refreshed, err := RefreshPaths(context.Background(), s, []string{target.Path}, 2)
	if err != nil || len(refreshed.Items) != 1 {
		t.Fatalf("refresh failed: %+v %v", refreshed, err)
	}
	if refreshed.Items[0].Fingerprint == target.Fingerprint {
		t.Fatal("selected path not refreshed")
	}
	inv, err := s.LoadInventory()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range inv.Items {
		if i.Path == other.Path && i.MeasuredAt != other.MeasuredAt {
			t.Fatal("unselected candidate was rescanned")
		}
	}
	if err := os.RemoveAll(target.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshPaths(context.Background(), s, []string{target.Path}, 1); err != nil {
		t.Fatal(err)
	}
	inv, err = s.LoadInventory()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range inv.Items {
		if i.Path == target.Path {
			t.Fatal("missing candidate remains active")
		}
	}
	h, err := s.History()
	if err != nil || h.Items != 0 {
		t.Fatal("external removal counted as our cleanup")
	}
}

func TestSameInodeAfterKnownDeletionStillGetsNewGeneration(t *testing.T) {
	_, _, s := fixture(t)
	item := Item{Path: "/fixture/target", Kind: "ignored", Bytes: 42, Identity: Identity{1, 2}, Fingerprint: "same", MeasuredAt: time.Now()}
	scan := Scan{ID: NewID(), CreatedAt: time.Now(), Items: []Item{item}}
	if err := s.SaveScan(scan); err != nil {
		t.Fatal(err)
	}
	item = scan.Items[0]
	p := Plan{ID: NewID(), CreatedAt: time.Now(), Items: []PlanItem{{Item: item, Status: "done"}}}
	if err := s.SavePlan(p); err != nil {
		t.Fatal(err)
	}
	scan.ID = NewID()
	scan.CreatedAt = time.Now()
	if err := s.SaveScan(scan); err != nil {
		t.Fatal(err)
	}
	if scan.Items[0].InventoryID == item.InventoryID {
		t.Fatal("path/inode reused deleted identity")
	}
	inv, err := s.LoadInventory()
	if err != nil || len(inv.Items) != 1 {
		t.Fatal("new observation was hidden")
	}
}

func TestFailedValidationRequiresTargetedRefresh(t *testing.T) {
	_, repo, s := fixture(t)
	path := filepath.Join(repo, "node_modules")
	write(t, filepath.Join(path, "output"), "old")
	scan := scanFixture(t, s, repo, 0)
	p := plan(t, scan, PlanOptions{Kind: "ignored"})
	write(t, filepath.Join(path, "output"), "changed content")
	if _, err := Apply(context.Background(), s, p, ApplyOptions{Yes: true}, nil); err == nil {
		t.Fatal("changed candidate should fail validation")
	}
	inv, err := s.LoadInventory()
	if err != nil || len(inv.Items) != 1 || !inv.Items[0].RefreshRequired {
		t.Fatal("failed candidate not marked for refresh")
	}
	next := plan(t, inv, PlanOptions{Kind: "ignored"})
	if len(next.Items) != 0 {
		t.Fatal("failed validation offered unchanged stale candidate again")
	}
}
