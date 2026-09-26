package cleaner

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db   *sql.DB
	lock *os.File
}

func NewID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func Open(path string) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// Reject symlinked state files; the database can contain private filesystem paths.
	for _, name := range []string{path, path + ".lock", path + "-wal", path + "-shm"} {
		if info, err := os.Lstat(name); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("state file is a symbolic link: %s", name)
		}
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("another invocation is using this database; retry after it finishes")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		lock.Close()
		return nil, err
	}
	file.Close()
	if err := os.Chmod(path, 0600); err != nil {
		lock.Close()
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", u.String()+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, lock: lock}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		s.Close()
		return nil, err
	}
	if version != 0 && version != 2 {
		s.Close()
		return nil, fmt.Errorf("database schema %d is unsupported; remove the old database and start over", version)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS scans (id TEXT PRIMARY KEY, created TEXT NOT NULL, data BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS plans (id TEXT PRIMARY KEY, data BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS measurements (path TEXT PRIMARY KEY, data BLOB NOT NULL);`)
	if err != nil {
		s.Close()
		return nil, err
	}
	if version == 0 {
		if err := s.initializeInventory(); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) Close() { s.db.Close(); s.lock.Close() }
func (s *Store) Cache() (map[string]Item, error) {
	rows, err := s.db.Query("SELECT data FROM measurements")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cache := map[string]Item{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var i Item
		if err := json.Unmarshal(b, &i); err != nil {
			return nil, err
		}
		cache[i.Path] = i
	}
	return cache, rows.Err()
}
func (s *Store) SaveScan(scan Scan) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for index := range scan.Items {
		item, err := observeItem(tx, scan.Items[index], scan.ID)
		if err != nil {
			return err
		}
		scan.Items[index] = item
		b, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT OR REPLACE INTO measurements VALUES (?,?)", item.Path, b); err != nil {
			return err
		}
	}
	for _, path := range scan.RemovedPaths {
		if err := reconcileInventory(tx, PlanItem{Item: Item{Path: path}, Status: "done"}, "", true); err != nil {
			return err
		}
	}
	b, err := json.Marshal(scan)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO scans VALUES (?,?,?)", scan.ID, scan.CreatedAt.Format("2006-01-02T15:04:05.000000000Z07:00"), b); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) LoadScan(id string) (Scan, error) {
	var b []byte
	var err error
	if id == "" {
		err = s.db.QueryRow("SELECT data FROM scans ORDER BY created DESC LIMIT 1").Scan(&b)
	} else {
		err = s.db.QueryRow("SELECT data FROM scans WHERE id=?", id).Scan(&b)
	}
	if err == sql.ErrNoRows {
		return Scan{}, fmt.Errorf("no matching scan; run 'devcleaner scan' first")
	}
	if err != nil {
		return Scan{}, err
	}
	var scan Scan
	err = json.Unmarshal(b, &scan)
	for i := range scan.Items {
		scan.Items[i] = deletionCandidate(scan.Items[i])
	}
	return scan, err
}
func (s *Store) SavePlan(p Plan) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous Plan
	var old []byte
	err = tx.QueryRow("SELECT data FROM plans WHERE id=?", p.ID).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if len(old) > 0 {
		if err := json.Unmarshal(old, &previous); err != nil {
			return err
		}
	}
	statuses := map[string]string{}
	for _, item := range previous.Items {
		statuses[item.Path] = item.Status
	}
	for index := range p.Items {
		item := &p.Items[index]
		if item.Status == statuses[item.Path] {
			continue
		}
		if item.Status == "failed" && item.InventoryID != "" {
			result, err := tx.Exec("UPDATE inventory SET state='needs_refresh' WHERE id=? AND state='present'", item.InventoryID)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n > 0 {
				if _, err := tx.Exec("DELETE FROM measurements WHERE path=?", item.Path); err != nil {
					return err
				}
			}
		}
		if item.Status == "done" || item.Status == "running" {
			item.MutationAt = time.Now().UTC()
			if err := reconcileInventory(tx, *item, p.ID, false); err != nil {
				return err
			}
		}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT OR REPLACE INTO plans VALUES (?,?)", p.ID, b); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) LoadPlan(id string) (Plan, error) {
	var b []byte
	err := s.db.QueryRow("SELECT data FROM plans WHERE id=?", id).Scan(&b)
	if err != nil {
		return Plan{}, fmt.Errorf("load plan %s: %w", id, err)
	}
	var p Plan
	err = json.Unmarshal(b, &p)
	return p, err
}
