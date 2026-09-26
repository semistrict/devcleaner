package cleaner

import (
	"os"
	"path/filepath"
	"strings"
)

// Managed storage is visible in the inventory, but never a direct-delete
// candidate. A VM image can contain the only copy of persistent user data.
type StorageRule struct {
	Tool    string `json:"tool"`
	Pattern string `json:"home_relative_pattern"`
	Note    string `json:"note"`
}

var StorageRules = []StorageRule{
	{"Docker", "Library/Containers/com.docker.docker/Data/vms", "Use Docker to identify unused images and build cache. Volumes and container filesystems may contain persistent data; the disk size is not a reclaimable-space estimate."},
	{"Lima", ".lima/*", "VM instance storage. Remove an unused instance through Lima only after preserving its data; allocated size is not a reclaimable-space estimate."},
	{"Colima", ".colima/_lima/*", "VM instance storage. Use Colima to manage instances and container data; allocated size is not a reclaimable-space estimate."},
	{"lnx", ".lnx*", "VM instances, snapshots, and test data. Test-work directories contain generated test fixtures; instance disks may contain persistent data. Select individual fixtures for cleanup rather than removing the entire store."},
}

func storageCandidate(path, home string) (Item, bool) {
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return Item{}, false
	}
	for _, rule := range StorageRules {
		matched, _ := filepath.Match(rule.Pattern, rel)
		if !matched {
			continue
		}
		// Lima's private configuration directory is not an instance.
		if (rule.Tool == "Lima" || rule.Tool == "Colima") && filepath.Base(path)[0] == '_' {
			continue
		}
		return Item{Path: path, Kind: "storage", Risk: "unsafe", Tool: rule.Tool, Action: "none", Blocked: "managed storage; direct deletion is disabled", Note: rule.Note}, true
	}
	return Item{}, false
}

func cacheCandidate(path, home string) (Item, bool) {
	rule, ok := cacheRule(path, home)
	if !ok {
		return Item{}, false
	}
	return Item{Path: path, Kind: "cache", Risk: rule.Safety, Tool: rule.Tool, Action: "delete", Note: "Tool cache. Close related builds and package managers before cleanup; the tool may download or rebuild it. Review caches may contain locally published packages."}, true
}

func homeCandidates(home string) []Item {
	items := []Item{}
	for _, rule := range CacheRules {
		path := filepath.Join(home, rule.RelativePath)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		item, _ := cacheCandidate(path, home)
		items = append(items, item)
	}
	for _, rule := range StorageRules {
		paths, _ := filepath.Glob(filepath.Join(home, rule.Pattern))
		for _, path := range paths {
			if item, ok := storageCandidate(path, home); ok {
				items = append(items, item)
			}
		}
	}
	return items
}

// Protect the whole storage boundary, including enclosing ignored directories
// and descendants. This is a lexical rule check, not a filesystem rescan, so
// saved plans cannot bypass newly recognized storage or hide it in a parent.
func managedStorageBoundary(path, home string) bool {
	if Contains(path, home) {
		return true
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, rule := range StorageRules {
		pattern := strings.Split(rule.Pattern, "/")
		match := true
		for i := 0; i < min(len(parts), len(pattern)); i++ {
			ok, _ := filepath.Match(pattern[i], parts[i])
			if !ok {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func protectManagedStorage(item Item, home string) Item {
	if classified, ok := storageCandidate(item.Path, home); ok {
		item.Kind, item.Risk, item.Tool = classified.Kind, classified.Risk, classified.Tool
		item.Action, item.Blocked, item.Note = classified.Action, classified.Blocked, classified.Note
	} else if managedStorageBoundary(item.Path, home) {
		item.Blocked = "contains or belongs to managed storage; direct deletion is disabled"
	}
	return item
}

// Inventory paths are canonical. Resolve HOME once at the calling boundary so
// a symlinked home cannot place its VM storage outside the protected namespace.
func canonicalHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return canonical(home)
}
