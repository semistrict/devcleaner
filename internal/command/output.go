package command

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"devcleaner/internal/cleaner"
)

type itemPage struct {
	ScanID    string         `json:"scan_id"`
	Total     int            `json:"total"`
	Next      *int           `json:"next_offset"`
	Items     []cleaner.Item `json:"items"`
	Partial   bool           `json:"partial"`
	Inventory bool           `json:"inventory"`
}

func size(bytes int64) string {
	value := float64(bytes)
	for _, unit := range []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"} {
		if value < 1024 || unit == "PiB" {
			if unit == "B" {
				return fmt.Sprintf("%d B", bytes)
			}
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return "0 B"
}

// Render control characters as visible escapes, never terminal instructions.
func visible(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '\uFFFD'
		}
		return r
	}, s)
}

func describeItem(w io.Writer, index int, item cleaner.Item) {
	kind := map[string]string{"worktree": "Worktree", "ignored": "Ignored artifacts", "cache": "Tool cache", "storage": "Managed storage"}[item.Kind]
	fmt.Fprintf(w, "%d. %s — %s — %s\n   Path: %s\n", index, kind, size(item.Bytes), item.Risk, strconv.Quote(item.Path))
	if item.Tool != "" {
		fmt.Fprintf(w, "   Tool: %s\n", visible(item.Tool))
	}
	if item.Branch != "" {
		fmt.Fprintf(w, "   Branch: %s (retained if the worktree is removed)\n", strconv.Quote(item.Branch))
	}
	if item.Repository != "" {
		fmt.Fprintf(w, "   Repository: %s\n", strconv.Quote(item.Repository))
	}
	if !item.Newest.IsZero() {
		fmt.Fprintf(w, "   Newest modification: %s (%d days ago)\n", item.Newest.Local().Format("2006-01-02 15:04"), max(0, int(time.Since(item.Newest).Hours()/24)))
	}
	if !item.MeasuredAt.IsZero() {
		label := "measured"
		if item.Cached {
			label = "cached estimate"
		}
		fmt.Fprintf(w, "   Size %s: %s; %s\n", label, item.MeasuredAt.Local().Format("2006-01-02 15:04"), countLabel(int(item.Files), "file"))
	}
	if item.RefreshRequired {
		fmt.Fprintln(w, "   Needs refresh: cleanup or a failed check invalidated this estimate; use refresh --path before planning.")
	}
	if item.Blocked != "" {
		fmt.Fprintf(w, "   Protected: %s\n", visible(item.Blocked))
	} else {
		action := "Unknown action; cannot apply"
		if item.Action == "delete" {
			action = "Permanently delete"
		}
		if item.Action == "trash" {
			action = "Obsolete Trash action; create a new plan"
		}
		if item.Action == "remove_worktree" {
			action = "Permanently remove through Git; keep the branch"
		}
		fmt.Fprintf(w, "   Proposed action: %s\n", action)
	}
	if item.Note != "" {
		fmt.Fprintf(w, "   Note: %s\n", visible(item.Note))
	}
}

func outputEnglish(w io.Writer, command string, data any, err error, database string) {
	switch d := data.(type) {
	case cleaner.CleanupHistory:
		fmt.Fprintf(w, "Cleanup history: %d items permanently removed; %s estimated allocated space removed. Actual free-space gains may differ.\n", d.Items, size(d.Bytes))
	case scanSummary:
		if d.Inventory {
			if d.Partial {
				fmt.Fprintln(w, "Partial scan stopped early; earlier saved inventory is retained.")
			}
			fmt.Fprintf(w, "Saved SQLite inventory: %d candidates; %s estimated remaining space, excluding overlaps and invalidated measurements.\n", d.Candidates, size(d.Bytes))
			fmt.Fprintf(w, "%d candidates need targeted refresh; %d are protected. No filesystem scan was performed.\n", d.RefreshRequired, d.Blocked)
			fmt.Fprintf(w, "Cleanup history: %d items removed; %s estimated allocated space removed.\n", d.History.Items, size(d.History.Bytes))
			printLargeIgnored(w, d.LargeIgnored)
			printLargeStorage(w, d.LargeStorage)
			fmt.Fprintln(w, "Use list or plan to continue, refresh --path to update selected items, or scan to discover new locations. Estimates can be stale; apply revalidates them.")
			break
		}
		fmt.Fprintf(w, "Scan %s\nSaved %s. Inspected %d working copies with %d workers in %s.\n", d.ID, d.Created.Local().Format("2006-01-02 15:04:05"), d.Repositories, d.Workers, time.Duration(d.Duration)*time.Millisecond)
		if d.Partial {
			fmt.Fprintf(w, "Partial scan: stopped early (%s). Saved %s with complete measurements; other locations remain unscanned.\n", visible(d.StopReason), countLabel(d.Candidates, "candidate"))
		}
		for _, root := range d.Roots {
			fmt.Fprintf(w, "Root: %s\n", strconv.Quote(root))
		}
		fmt.Fprintf(w, "Found %d candidates: %d worktrees, %d ignored artifacts, %d tool caches, %d managed storage locations.\n", d.Candidates, d.Kinds["worktree"], d.Kinds["ignored"], d.Kinds["cache"], d.Kinds["storage"])
		fmt.Fprintf(w, "Estimated candidate space: %s, excluding overlaps. %d candidates are protected.\n", size(d.Bytes), d.Blocked)
		fmt.Fprintf(w, "Reused %d cached measurements. This inventory is not a deletion plan.\n", d.CacheHits)
		printLargeIgnored(w, d.LargeIgnored)
		printLargeStorage(w, d.LargeStorage)
		for _, warning := range d.Warnings {
			fmt.Fprintf(w, "Warning: %s\n", visible(warning))
		}
		fmt.Fprintln(w, "Use 'devcleaner list' to inspect candidates or 'devcleaner plan --older-than 7d' to propose cleanup.")
	case itemPage:
		if d.Inventory {
			fmt.Fprintf(w, "Saved SQLite inventory: showing %d of %d matches; no filesystem scan.\n", len(d.Items), d.Total)
		} else {
			fmt.Fprintf(w, "Candidates from scan %s: showing %d of %d matches.\n", d.ScanID, len(d.Items), d.Total)
		}
		if d.Partial {
			fmt.Fprintln(w, "This is a partial scan. Only completed measurements are listed; other locations remain unscanned.")
		}
		for i, item := range d.Items {
			fmt.Fprintln(w)
			describeItem(w, i+1, item)
		}
		if d.Next != nil {
			fmt.Fprintf(w, "\nMore matches remain. Repeat your list command with --offset %d.\n", *d.Next)
		}
		if len(d.Items) == 0 {
			fmt.Fprintln(w, "No candidates match this page and its filters.")
		}
	case cleaner.Plan:
		fmt.Fprintf(w, "Cleanup plan %s\nBased on scan %s. Maximum safety: %s. Minimum age: %s.\n", d.ID, d.ScanID, d.Safety, ageDescription(time.Duration(d.OlderThanSeconds)*time.Second))
		fmt.Fprintf(w, "%s selected; estimated reclaimable space: %s.\n", countLabel(len(d.Items), "item"), size(d.EstimatedBytes))
		legacy := false
		for _, item := range d.Items {
			legacy = legacy || item.Action == "trash"
		}
		if legacy {
			fmt.Fprintln(w, "This legacy plan cannot be applied. Create a new plan to review permanent deletion.")
		} else {
			fmt.Fprintf(w, "%s selected for permanent deletion of artifacts and caches. No files will be moved to Trash.\n", size(d.DeletedBytes))
		}
		fmt.Fprintf(w, "%s would be removed permanently through Git; branches are retained.\n", size(d.WorktreeBytes))
		fmt.Fprintf(w, "Expires %s.\n", d.ExpiresAt.Local().Format("2006-01-02 15:04:05"))
		counts := map[string]int{}
		for i, item := range d.Items {
			fmt.Fprintln(w)
			describeItem(w, i+1, item.Item)
			fmt.Fprintf(w, "   Status: %s\n", visible(item.Status))
			counts[item.Status]++
			if item.Error != "" {
				fmt.Fprintf(w, "   Problem: %s\n", visible(item.Error))
			}
			if item.TrashPath != "" {
				fmt.Fprintf(w, "   Recover from: %s\n", strconv.Quote(item.TrashPath))
			}
		}
		fmt.Fprintf(w, "\n%d pending, %d completed, %d failed checks, %d requiring reconciliation.\n", counts["pending"], counts["done"], counts["failed"], counts["running"])
		if command == "plan" {
			fmt.Fprintln(w, "No files have been changed. Inspect this plan before approving it.")
		}
		if !legacy && counts["running"] == 0 && (counts["pending"] > 0 || counts["failed"] > 0) {
			suffix := ""
			if database != "" {
				suffix = " --db " + shellQuote(database)
			}
			fmt.Fprintf(w, "Inspect with: devcleaner show --plan %s%s\n", d.ID, suffix)
			fmt.Fprintf(w, "Apply with: devcleaner apply --plan %s%s --yes", d.ID, suffix)
			level := 0
			for _, i := range d.Items {
				if i.Status != "done" {
					level = max(level, cleaner.SafetyRank(i.Risk))
				}
			}
			if level == 2 {
				fmt.Fprint(w, " --allow-unsafe")
			} else if level == 1 {
				fmt.Fprint(w, " --allow-review")
			}
			fmt.Fprintln(w)
		}
		for _, note := range d.Notes {
			fmt.Fprintf(w, "Note: %s\n", visible(note))
		}
	case map[string]string:
		if command == "version" {
			fmt.Fprintf(w, "DevCleaner %s\n", d["version"])
		}
	case stopResult:
		fmt.Fprintln(w, d.Message)
	case map[string]any:
		if command == "rules" {
			fmt.Fprintln(w, "Safety levels: safe (recognized generated files), review (inspect first), unsafe (local data). Protected worktrees cannot be removed at any level.")
			fmt.Fprintln(w, "\nRecognized Git-ignored directories:")
			for _, rule := range cleaner.ArtifactRules {
				fmt.Fprintf(w, "- %s: %s [%s]. %s", rule.Tool, strings.Join(rule.Names, ", "), rule.Safety, rule.Note)
				if len(rule.Markers) > 0 {
					fmt.Fprintf(w, " Requires a project marker: %s.", strings.Join(rule.Markers, ", "))
				}
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, "\nManaged storage (scan --caches; direct deletion disabled):")
			for _, rule := range cleaner.StorageRules {
				fmt.Fprintf(w, "- %s: ~/%s — %s\n", rule.Tool, rule.Pattern, rule.Note)
			}
			fmt.Fprintln(w, "\nOptional tool caches (scan --caches):")
			for _, rule := range cleaner.CacheRules {
				fmt.Fprintf(w, "- %s: ~/%s [%s]\n", rule.Tool, rule.RelativePath, rule.Safety)
			}
		}
	}
	if err != nil {
		fmt.Fprintf(w, "Error: %s\n", visible(err.Error()))
	}
}

func countLabel(n int, word string) string {
	if n != 1 {
		word += "s"
	}
	return fmt.Sprintf("%d %s", n, word)
}
func ageDescription(d time.Duration) string {
	for _, unit := range []struct {
		duration time.Duration
		name     string
	}{{24 * time.Hour, "day"}, {time.Hour, "hour"}, {time.Minute, "minute"}, {time.Second, "second"}} {
		if d >= unit.duration && d%unit.duration == 0 {
			return countLabel(int(d/unit.duration), unit.name)
		}
	}
	return fmt.Sprintf("%g seconds", d.Seconds())
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
