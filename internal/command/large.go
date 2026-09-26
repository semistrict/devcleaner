package command

import (
	"devcleaner/internal/cleaner"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// Discovery prominence is independent of permission to delete. Unknown names,
// recent activity, and protection reasons must not hide large ignored data.
func largeIgnored(items []cleaner.Item) []cleaner.Item {
	large := []cleaner.Item{}
	for _, item := range items {
		if item.Kind == "ignored" && item.Bytes >= 1<<30 {
			large = append(large, item)
		}
	}
	sort.Slice(large, func(i, j int) bool {
		if large[i].Bytes == large[j].Bytes {
			return large[i].Path < large[j].Path
		}
		return large[i].Bytes > large[j].Bytes
	})
	return large[:min(5, len(large))]
}

func printLargeIgnored(w io.Writer, items []cleaner.Item) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintln(w, "Largest Git-ignored data (at least 1 GiB; includes unclassified and protected items):")
	for _, item := range items {
		label := "generated output"
		if item.Risk == "review" {
			label = "review classification"
		}
		if item.Risk == "unsafe" {
			label = "unclassified ignored data"
		}
		if item.RefreshRequired {
			label += "; previous estimate, refresh required"
		} else if item.Fingerprint == "" {
			label += "; incomplete size, at least the amount shown"
		}
		if item.Blocked != "" {
			label += "; " + visible(item.Blocked)
		}
		fmt.Fprintf(w, "  %s  %s — %s\n", size(item.Bytes), strconv.Quote(item.Path), label)
	}
	fmt.Fprintln(w, "See all: devcleaner list --kind ignored --min-bytes 1073741824. Sizes are saved estimates; these findings are not deletion approval.")
}

func largeStorage(items []cleaner.Item) []cleaner.Item {
	selected := []cleaner.Item{}
	for _, item := range items {
		if item.Kind == "storage" && item.Bytes >= 1<<30 {
			selected = append(selected, item)
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Bytes == selected[j].Bytes {
			return selected[i].Path < selected[j].Path
		}
		return selected[i].Bytes > selected[j].Bytes
	})
	return selected[:min(5, len(selected))]
}

func printLargeStorage(w io.Writer, items []cleaner.Item) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintln(w, "Large managed storage (allocated size, not estimated savings; direct deletion disabled):")
	for _, item := range items {
		label := visible(item.Tool)
		if item.RefreshRequired {
			label += "; previous estimate, refresh required"
		} else if item.Fingerprint == "" {
			label += "; incomplete size"
		}
		fmt.Fprintf(w, "  %s  %s — %s\n", size(item.Bytes), strconv.Quote(item.Path), label)
	}
	fmt.Fprintln(w, "See cleanup guidance: devcleaner list --kind storage --min-bytes 1073741824.")
}
