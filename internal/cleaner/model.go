package cleaner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const Version = "0.2.0"

type Identity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type Item struct {
	InventoryID     string    `json:"inventory_id,omitempty"`
	Path            string    `json:"path"`
	Kind            string    `json:"kind"`
	Risk            string    `json:"safety"`
	Tool            string    `json:"tool,omitempty"`
	Action          string    `json:"action"`
	Bytes           int64     `json:"allocated_bytes"`
	Files           int64     `json:"files"`
	Newest          time.Time `json:"newest_modification"`
	MeasuredAt      time.Time `json:"measured_at"`
	Cached          bool      `json:"cached"`
	Identity        Identity  `json:"identity"`
	Fingerprint     string    `json:"fingerprint"`
	Repository      string    `json:"repository,omitempty"`
	Branch          string    `json:"branch,omitempty"`
	Blocked         string    `json:"blocked_reason,omitempty"`
	Note            string    `json:"note"`
	RefreshRequired bool      `json:"refresh_required,omitempty"`
}
type Scan struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	Roots        []string  `json:"roots"`
	Workers      int       `json:"workers"`
	Repositories int       `json:"repositories"`
	CacheHits    int       `json:"cache_hits"`
	DurationMS   int64     `json:"duration_ms"`
	Items        []Item    `json:"items"`
	Warnings     []string  `json:"warnings"`
	Partial      bool      `json:"partial"`
	StopReason   string    `json:"stop_reason,omitempty"`
	Inventory    bool      `json:"inventory,omitempty"`
	RemovedPaths []string  `json:"removed_paths,omitempty"`
}
type PlanItem struct {
	Item
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	TrashPath  string    `json:"trash_path,omitempty"`
	MutationAt time.Time `json:"mutation_at,omitempty"`
}
type Plan struct {
	Invalidated      bool       `json:"invalidated,omitempty"`
	ID               string     `json:"id"`
	ScanID           string     `json:"scan_id"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	OlderThanSeconds int64      `json:"older_than_seconds"`
	Safety           string     `json:"maximum_safety"`
	EstimatedBytes   int64      `json:"estimated_reclaimable_bytes"`
	TrashBytes       int64      `json:"bytes_reclaimable_after_emptying_trash,omitempty"` // Legacy plan history only.
	DeletedBytes     int64      `json:"bytes_deleted_directly"`
	WorktreeBytes    int64      `json:"bytes_removed_by_git"`
	Items            []PlanItem `json:"items"`
	Notes            []string   `json:"notes"`
}

func Contains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Outermost preserves order while excluding nested candidates in O(items ×
// path depth), rather than comparing every file with every other file.
func Outermost(items []Item) []Item {
	paths := make(map[string]bool, len(items))
	for _, item := range items {
		paths[item.Path] = true
	}
	result := []Item{}
	emitted := map[string]bool{}
	for _, item := range items {
		covered := false
		for parent := filepath.Dir(item.Path); parent != item.Path; parent = filepath.Dir(parent) {
			if paths[parent] {
				covered = true
				break
			}
			if parent == filepath.Dir(parent) {
				break
			}
		}
		if !covered && !emitted[item.Path] {
			result = append(result, item)
			emitted[item.Path] = true
		}
	}
	return result
}

// parallelMap bounds both concurrent filesystem traversals and Git subprocesses.
// Each worker owns one job at a time; there is no goroutine per discovered file.
func parallelMap[A, B any](ctx context.Context, workers int, input []A, fn func(context.Context, A) B) []B {
	output := make([]B, len(input))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, len(input)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				output[i] = fn(ctx, input[i])
			}
		}()
	}
	for i := range input {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return output
		}
	}
	close(jobs)
	wg.Wait()
	return output
}

func ValidateWorkers(n int) error {
	if n < 1 || n > 32 {
		return fmt.Errorf("workers must be between 1 and 32")
	}
	return nil
}

// Inventories describe candidates, not approved actions. Reuse old measurements
// when making a new deletion plan, but never rewrite a saved plan's actions.
func deletionCandidate(item Item) Item {
	if item.Action == "trash" && (item.Kind == "ignored" || item.Kind == "cache") {
		item.Action = "delete"
		item.Note = strings.ReplaceAll(item.Note, "Trash is recoverable until emptied;", "Deletion is permanent;")
	}
	return item
}
