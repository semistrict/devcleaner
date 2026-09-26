package cleaner

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type inventoryRow struct {
	ID, State string
	Item      Item
}

func inventoryRows(tx *sql.Tx, path string) ([]inventoryRow, error) {
	query := "SELECT id,state,data FROM inventory WHERE state IN ('present','needs_refresh')"
	var args []any
	if path != "" {
		query += " AND (path=? OR instr(path,?||'/')=1 OR instr(?,path||'/')=1)"
		args = []any{path, path, path}
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []inventoryRow{}
	for rows.Next() {
		var r inventoryRow
		var b []byte
		if err := rows.Scan(&r.ID, &r.State, &b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &r.Item); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func observeItem(tx *sql.Tx, item Item, scanID string) (Item, error) {
	var id string
	var oldData []byte
	err := tx.QueryRow("SELECT id,data FROM inventory WHERE path=? AND state IN ('present','needs_refresh')", item.Path).Scan(&id, &oldData)
	if err != nil && err != sql.ErrNoRows {
		return item, err
	}
	if err == nil {
		var old Item
		if err := json.Unmarshal(oldData, &old); err != nil {
			return item, err
		}
		if old.Identity != item.Identity {
			if _, err := tx.Exec("UPDATE inventory SET state='superseded' WHERE id=?", id); err != nil {
				return item, err
			}
			id = ""
		}
	}
	// A deleted or reset entry is never revived, even if the inode is reused.
	if id == "" {
		id = NewID()
	}
	item.InventoryID = id
	b, err := json.Marshal(item)
	if err != nil {
		return item, err
	}
	state := "present"
	if item.RefreshRequired {
		state = "needs_refresh"
	}
	_, err = tx.Exec("INSERT INTO inventory(id,path,state,scan_id,data) VALUES (?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,scan_id=excluded.scan_id,data=excluded.data", id, item.Path, state, scanID, b)
	return item, err
}

// Reconcile the journal with inventory in the same transaction. Historical
// generations and metadata remain intact; path strings are not object IDs.
func reconcileInventory(tx *sql.Tx, item PlanItem, planID string, missing bool) error {
	rows, err := inventoryRows(tx, item.Path)
	if err != nil {
		return err
	}
	matched := ""
	for _, r := range rows {
		if r.Item.Path == item.Path && (item.InventoryID != "" && r.ID == item.InventoryID) {
			matched = r.ID
			break
		}
	}
	for _, r := range rows {
		descendant := Contains(item.Path, r.Item.Path)
		ancestor := r.Item.Path != item.Path && Contains(r.Item.Path, item.Path)
		if ancestor && !item.MutationAt.IsZero() && r.Item.MeasuredAt.After(item.MutationAt) {
			continue
		}
		if !descendant && !ancestor {
			continue
		}
		if descendant && !missing {
			if r.Item.Path == item.Path && r.ID != matched {
				continue
			}
			if r.Item.Path != item.Path && !item.MutationAt.IsZero() && r.Item.MeasuredAt.After(item.MutationAt) {
				continue
			}
		}
		state := "needs_refresh"
		if descendant && (item.Status == "done" || missing) {
			state = "deleted"
		}
		if descendant && missing {
			state = "missing"
		}
		r.Item.RefreshRequired = state == "needs_refresh"
		b, err := json.Marshal(r.Item)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE inventory SET state=?,data=?,changed_at=? WHERE id=?", state, b, time.Now().UTC().Format(time.RFC3339Nano), r.ID); err != nil {
			return err
		}
		// This is only a disposable measurement cache; inventory keeps all metadata.
		if _, err := tx.Exec("DELETE FROM measurements WHERE path=?", r.Item.Path); err != nil {
			return err
		}
	}
	if item.Status == "done" && !missing {
		id := matched
		if id == "" {
			id = item.InventoryID
			if id == "" {
				id = NewID()
			}
			item.InventoryID = id
			b, err := json.Marshal(item.Item)
			if err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT OR IGNORE INTO inventory(id,path,state,scan_id,data) VALUES (?,?,'deleted','',?)", id, item.Path, b); err != nil {
				return err
			}
		}
		_, err = tx.Exec("UPDATE inventory SET state='deleted',plan_id=?,deleted_bytes=?,changed_at=? WHERE id=?", planID, item.Bytes, item.MutationAt.Format(time.RFC3339Nano), id)
	}
	return err
}

func (s *Store) initializeInventory() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE inventory(id TEXT PRIMARY KEY,path TEXT NOT NULL,state TEXT NOT NULL,scan_id TEXT NOT NULL,data BLOB NOT NULL,plan_id TEXT NOT NULL DEFAULT '',deleted_bytes INTEGER NOT NULL DEFAULT 0,changed_at TEXT NOT NULL DEFAULT '');
 CREATE UNIQUE INDEX inventory_current_path ON inventory(path) WHERE state IN ('present','needs_refresh');
 PRAGMA user_version=2;`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LoadInventory() (Scan, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Scan{}, err
	}
	defer tx.Rollback()
	rows, err := inventoryRows(tx, "")
	if err != nil {
		return Scan{}, err
	}
	scan := Scan{ID: "inventory", Inventory: true, Items: []Item{}}
	var latest []byte
	err = tx.QueryRow("SELECT data FROM scans ORDER BY created DESC LIMIT 1").Scan(&latest)
	if err != nil && err != sql.ErrNoRows {
		return Scan{}, err
	}
	if len(latest) > 0 {
		var saved Scan
		if err := json.Unmarshal(latest, &saved); err != nil {
			return Scan{}, err
		}
		scan.Partial = saved.Partial
		scan.StopReason = saved.StopReason
	}
	for _, r := range rows {
		item := deletionCandidate(r.Item)
		item.InventoryID = r.ID
		item.Cached = true
		item.RefreshRequired = r.State == "needs_refresh"
		scan.Items = append(scan.Items, item)
		if item.MeasuredAt.After(scan.CreatedAt) {
			scan.CreatedAt = item.MeasuredAt
		}
	}
	sort.Slice(scan.Items, func(i, j int) bool {
		if scan.Items[i].Bytes == scan.Items[j].Bytes {
			return scan.Items[i].Path < scan.Items[j].Path
		}
		return scan.Items[i].Bytes > scan.Items[j].Bytes
	})
	return scan, nil
}

type CleanupHistory struct {
	Items int   `json:"items"`
	Bytes int64 `json:"estimated_deleted_bytes"`
}

func (s *Store) History() (CleanupHistory, error) {
	var h CleanupHistory
	err := s.db.QueryRow("SELECT count(*),coalesce(sum(deleted_bytes),0) FROM inventory WHERE state='deleted' AND plan_id!=''").Scan(&h.Items, &h.Bytes)
	return h, err
}

func (s *Store) ResetInventory() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE inventory SET state='superseded' WHERE state IN ('present','needs_refresh')"); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM measurements"); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT data FROM plans")
	if err != nil {
		return err
	}
	plans := []Plan{}
	for rows.Next() {
		var b []byte
		var p Plan
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(b, &p); err != nil {
			rows.Close()
			return err
		}
		plans = append(plans, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range plans {
		p.Invalidated = true
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE plans SET data=? WHERE id=?", b, p.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) checkGeneration(item Item) error {
	if item.InventoryID == "" {
		return nil
	} // Legacy plans still undergo full filesystem validation.
	var state string
	if err := s.db.QueryRow("SELECT state FROM inventory WHERE id=?", item.InventoryID).Scan(&state); err != nil {
		return err
	}
	if state != "present" {
		return fmt.Errorf("inventory generation is %s; refresh this path and create a new plan", state)
	}
	return nil
}
