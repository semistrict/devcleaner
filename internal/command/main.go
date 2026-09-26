package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"devcleaner/internal/cleaner"
)

const help = `devcleaner — persistent, parallel disk cleanup for developer workspaces

Usage: devcleaner <command> [options]

Commands:
  scan    Discover repositories, linked worktrees, and ignored artifacts
  stop    Stop the active scan and save completed measurements
  status  Summarize the SQLite inventory and cleanup history; no disk scan
  refresh Measure selected known paths or recognized tool locations
  history Show estimated space removed by completed cleanup
  reset   Start a fresh inventory while keeping all history
  rules   List language artifact rules, cache paths, and safety levels
  list    List saved candidates (paginated; includes protected candidates)
  plan    Save a non-overlapping cleanup plan; changes no files
  show    Read a saved plan and its execution journal
  apply   Revalidate and execute an explicitly approved saved plan
  version Print the CLI version

Results and errors are written in readable English.
--db PATH works on every command. --json opts into structured output.
The CLI uses the running menu-bar app when available. --app requires the app;
--standalone runs directly in the terminal process.
Progress is written to stderr every second. --quiet disables progress.
No prompts, terminal UI, automatic cleanup, or background service.

Examples:
  devcleaner scan --workers 8
  devcleaner status
  devcleaner refresh --path /path/to/repo/target
  devcleaner list --kind worktree --older-than 30d
  devcleaner plan --kind ignored --older-than 14d
  devcleaner plan --kind worktree --older-than 30d
  devcleaner show --plan PLAN_ID
  devcleaner apply --plan PLAN_ID --yes --allow-review

scan:   --root PATH (repeatable; default home directory), --workers N (1–32),
        --cache-ttl 24h, --refresh, --caches, --max-depth 8, --time-limit 30s
        Ctrl-C or 'devcleaner stop' saves completed results as a partial scan.
refresh: --path PATH (repeatable, required), --workers N, --time-limit 30s
reset:  --yes (retires the current inventory and invalidates old plans; keeps history)
list:   --scan ID (optional historical snapshot; default current inventory), --kind worktree|ignored|cache|storage,
        --older-than 0d, --min-bytes 0, --limit 100, --offset 0
plan:   --scan ID, --kind worktree|ignored|cache, --older-than 30d,
        --min-bytes 0, --limit 0 (unlimited), --path PATH (repeatable),
        --safety safe|review|unsafe (default review)
show:   --plan ID
apply:  --plan ID, --yes, --allow-review (clean worktrees / review caches),
        --allow-unsafe (unrecognized local data). Without --yes, nothing changes.

Safety: safe = recognized generated artifacts; review = clean named worktrees
and potentially modified caches; unsafe = local data or protected worktrees.
Dirty, locked, detached, and submodule worktrees stay blocked at every level.

Ignored artifacts and caches are permanently deleted to reclaim disk space.
Git worktree removal is permanent and retains the branch. It refuses dirty, locked, detached, or submodule worktrees.
Close agents, builds, and watchers using selected items before applying.
Exit codes: 0 success, 1 operation/partial failure, 2 invalid arguments.
`

type envelope struct {
	Schema  int        `json:"schema_version"`
	Command string     `json:"command"`
	Data    any        `json:"data,omitempty"`
	Error   *errorInfo `json:"error,omitempty"`
}
type errorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type paths []string

func (p *paths) String() string     { return strings.Join(*p, ",") }
func (p *paths) Set(v string) error { *p = append(*p, v); return nil }
func duration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, err
		}
		return time.ParseDuration(strconv.FormatFloat(n*24, 'f', -1, 64) + "h")
	}
	return time.ParseDuration(s)
}
func outputJSON(w io.Writer, command string, data any, err error, code string) {
	e := envelope{Schema: 1, Command: command, Data: data}
	if err != nil {
		e.Error = &errorInfo{Code: code, Message: err.Error()}
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(e)
}

func loadCandidates(s *cleaner.Store, id string) (cleaner.Scan, error) {
	if id != "" {
		return s.LoadScan(id)
	}
	return s.LoadInventory()
}

type scanSummary struct {
	Inventory       bool                   `json:"inventory"`
	RefreshRequired int                    `json:"refresh_required"`
	History         cleaner.CleanupHistory `json:"cleanup_history"`
	ID              string                 `json:"scan_id"`
	Created         time.Time              `json:"created_at"`
	Roots           []string               `json:"roots"`
	Repositories    int                    `json:"repositories"`
	Workers         int                    `json:"workers"`
	Duration        int64                  `json:"duration_ms"`
	Candidates      int                    `json:"candidates"`
	Blocked         int                    `json:"blocked_candidates"`
	CacheHits       int                    `json:"cached_measurements"`
	Bytes           int64                  `json:"estimated_candidate_bytes_without_overlap"`
	Kinds           map[string]int         `json:"kinds"`
	Warnings        []string               `json:"warnings"`
	Note            string                 `json:"note"`
	Partial         bool                   `json:"partial"`
	StopReason      string                 `json:"stop_reason,omitempty"`
	LargeIgnored    []cleaner.Item         `json:"large_ignored"`
	LargeStorage    []cleaner.Item         `json:"large_storage"`
}

func summarize(scan cleaner.Scan) any {
	var bytes int64
	blocked := 0
	kindCounts := map[string]int{}
	// Items can overlap in the inventory; sum only the outermost candidates.
	for _, i := range scan.Items {
		kindCounts[i.Kind]++
		if i.Blocked != "" {
			blocked++
		}
	}
	measured := []cleaner.Item{}
	stale := 0
	for _, i := range scan.Items {
		if i.RefreshRequired {
			stale++
		} else {
			measured = append(measured, i)
		}
	}
	for _, i := range cleaner.Outermost(measured) {
		bytes += i.Bytes
	}
	return scanSummary{scan.Inventory, stale, cleaner.CleanupHistory{}, scan.ID, scan.CreatedAt, scan.Roots, scan.Repositories, scan.Workers, scan.DurationMS, len(scan.Items), blocked, scan.CacheHits, bytes, kindCounts, scan.Warnings, "Inventory includes overlapping worktrees and their artifacts. Plans remove overlaps. Saved measurements may be stale; use refresh --path for selected current sizes. New locations require explicit scan discovery.", scan.Partial, scan.StopReason, largeIgnored(scan.Items), largeStorage(scan.Items)}
}

func run(ctx context.Context, args []string, out, progressOut io.Writer) (exitCode int) {
	partialScan := false
	var dbPath string
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		}
	}
	output := func(w io.Writer, command string, data any, err error, code string) {
		if jsonOutput {
			outputJSON(w, command, data, err, code)
		} else {
			outputEnglish(w, command, data, err, dbPath)
		}
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, help)
		return 0
	}
	home, err := os.UserHomeDir()
	if err != nil {
		output(out, "", nil, err, "environment")
		return 1
	}
	dbPath = os.Getenv("DEVCLEANER_DB")
	if dbPath == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			output(out, "", nil, err, "environment")
			return 1
		}
		dbPath = filepath.Join(config, "devcleaner", "state.db")
	}
	cleanArgs := []string{}
	quiet := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--quiet":
			quiet = true
		case args[i] == "--json":
			continue
		case args[i] == "--db":
			if i+1 == len(args) {
				output(out, "", nil, fmt.Errorf("--db requires a path"), "invalid_arguments")
				return 2
			}
			i++
			dbPath = args[i]
		case strings.HasPrefix(args[i], "--db="):
			dbPath = strings.TrimPrefix(args[i], "--db=")
		default:
			cleanArgs = append(cleanArgs, args[i])
		}
	}
	if len(cleanArgs) == 0 {
		fmt.Fprint(out, help)
		return 0
	}
	command := cleanArgs[0]
	if command == "version" {
		output(out, command, map[string]string{"version": cleaner.Version}, nil, "")
		return 0
	}
	if command == "rules" {
		output(out, command, map[string]any{"artifact_rules": cleaner.ArtifactRules, "cache_rules": cleaner.CacheRules, "storage_rules": cleaner.StorageRules, "safety_levels": []string{"safe", "review", "unsafe"}, "note": "Artifact rules apply only to Git-ignored directories. Protected worktrees cannot be removed at any level."}, nil, "")
		return 0
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var roots, selectedPaths paths
	var workers, maxDepth, limit, offset int
	var scanID, planID, kind, age, ttl, safety, timeLimit string
	var minBytes int64
	var refresh, caches, yes, allowReview, allowUnsafe bool
	switch command {
	case "scan":
		fs.Var(&roots, "root", "scan root")
		fs.IntVar(&workers, "workers", min(8, max(2, runtime.NumCPU())), "parallel workers")
		fs.IntVar(&maxDepth, "max-depth", 8, "discovery depth")
		fs.StringVar(&ttl, "cache-ttl", "24h", "measurement reuse window")
		fs.BoolVar(&refresh, "refresh", false, "remeasure all candidates")
		fs.BoolVar(&caches, "caches", false, "include known tool caches and managed storage")
		fs.StringVar(&timeLimit, "time-limit", "0s", "stop after this duration and save completed measurements; zero disables")
	case "refresh":
		fs.Var(&selectedPaths, "path", "exact candidate or recognized tool location to measure")
		fs.IntVar(&workers, "workers", min(8, max(2, runtime.NumCPU())), "parallel workers")
		fs.StringVar(&timeLimit, "time-limit", "0s", "save partial refresh after this duration")
	case "reset":
		fs.BoolVar(&yes, "yes", false, "start fresh inventory, retaining history")
	case "status", "stop", "history":
	case "list", "plan":
		fs.StringVar(&scanID, "scan", "", "scan ID")
		fs.StringVar(&kind, "kind", "", "candidate kind")
		if command == "list" {
			fs.Int64Var(&minBytes, "min-bytes", 0, "minimum allocated bytes, regardless of safety classification")
			fs.StringVar(&age, "older-than", "0d", "age")
			fs.IntVar(&limit, "limit", 100, "page size")
			fs.IntVar(&offset, "offset", 0, "offset")
		} else {
			fs.StringVar(&age, "older-than", "30d", "age")
			fs.IntVar(&limit, "limit", 0, "maximum plan items")
			fs.Int64Var(&minBytes, "min-bytes", 0, "minimum allocated bytes")
			fs.StringVar(&safety, "safety", "review", "maximum safety level")
			fs.Var(&selectedPaths, "path", "exact candidate path")
		}
	case "show", "apply":
		fs.StringVar(&planID, "plan", "", "plan ID")
		if command == "apply" {
			fs.BoolVar(&yes, "yes", false, "approve mutation")
			fs.BoolVar(&allowReview, "allow-review", false, "approve review items")
			fs.BoolVar(&allowUnsafe, "allow-unsafe", false, "approve unsafe local data")
		}
	default:
		output(out, command, nil, fmt.Errorf("unknown command; run devcleaner help"), "invalid_arguments")
		return 2
	}
	if err := fs.Parse(cleanArgs[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(out, help)
			return 0
		}
		output(out, command, nil, err, "invalid_arguments")
		return 2
	}
	if fs.NArg() != 0 {
		output(out, command, nil, fmt.Errorf("unexpected positional arguments; use named options"), "invalid_arguments")
		return 2
	}
	if (command == "show" || command == "apply") && planID == "" {
		output(out, command, nil, fmt.Errorf("--plan ID is required"), "invalid_arguments")
		return 2
	}
	if kind != "" && kind != "worktree" && kind != "ignored" && kind != "cache" && !(command == "list" && kind == "storage") {
		output(out, command, nil, fmt.Errorf("invalid --kind"), "invalid_arguments")
		return 2
	}
	if safety != "" && cleaner.SafetyRank(safety) < 0 {
		output(out, command, nil, fmt.Errorf("safety must be safe, review, or unsafe"), "invalid_arguments")
		return 2
	}
	var ageDuration, cacheTTL, scanLimit time.Duration
	if timeLimit != "" {
		scanLimit, err = duration(timeLimit)
		if err != nil || scanLimit < 0 {
			output(out, command, nil, fmt.Errorf("invalid nonnegative --time-limit duration"), "invalid_arguments")
			return 2
		}
	}
	if age != "" {
		ageDuration, err = duration(age)
		if err != nil || ageDuration < 0 {
			output(out, command, nil, fmt.Errorf("invalid nonnegative --older-than duration"), "invalid_arguments")
			return 2
		}
	}
	if ttl != "" {
		cacheTTL, err = duration(ttl)
		if err != nil || cacheTTL < 0 {
			output(out, command, nil, fmt.Errorf("invalid nonnegative --cache-ttl duration"), "invalid_arguments")
			return 2
		}
	}
	if limit < 0 || offset < 0 || minBytes < 0 {
		output(out, command, nil, fmt.Errorf("numeric filters must be nonnegative"), "invalid_arguments")
		return 2
	}
	if command == "scan" || command == "refresh" {
		if err := cleaner.ValidateWorkers(workers); err != nil {
			output(out, command, nil, err, "invalid_arguments")
			return 2
		}
		if maxDepth < 0 || maxDepth > 64 {
			output(out, command, nil, fmt.Errorf("max-depth must be between 0 and 64"), "invalid_arguments")
			return 2
		}
	}
	if !quiet {
		progress := startProgress(progressOut, command, time.Second)
		ctx = cleaner.WithProgress(ctx, progress.update)
		defer func() { progress.finish(exitCode, ctx.Err() != nil, partialScan) }()
	}
	if command == "stop" {
		err := requestScanStop(dbPath)
		var result any
		if err == nil {
			result = stopResult{Message: "Stop requested. The scan is saving completed measurements. Once it exits, use 'devcleaner status', 'list', or 'plan' with the same database."}
		}
		output(out, command, result, err, "stop_failed")
		if err != nil {
			return 1
		}
		return 0
	}
	cleaner.ReportProgress(ctx, "Opening state database", 0, 0)
	s, err := cleaner.Open(dbPath)
	if err != nil {
		output(out, command, nil, err, "state_unavailable")
		return 1
	}
	defer s.Close()
	cleaner.ReportProgress(ctx, "Loading saved state", 0, 0)
	var data any
	switch command {
	case "scan", "refresh":
		scanContext, cancel := context.WithCancel(ctx)
		defer cancel()
		if scanLimit > 0 {
			var deadlineCancel context.CancelFunc
			scanContext, deadlineCancel = context.WithTimeout(scanContext, scanLimit)
			defer deadlineCancel()
		}
		closeControl, controlErr := startScanControl(dbPath, cancel)
		if controlErr != nil {
			output(out, command, nil, controlErr, "scan_control_unavailable")
			return 1
		}
		defer closeControl()
		if len(roots) == 0 {
			roots = append(roots, home)
		}
		if refresh {
			cacheTTL = 0
		}
		var scan cleaner.Scan
		if command == "refresh" {
			scan, err = cleaner.RefreshPaths(scanContext, s, selectedPaths, workers)
		} else {
			scan, err = cleaner.ScanDisk(scanContext, s, cleaner.ScanOptions{Roots: roots, Workers: workers, CacheTTL: cacheTTL, Caches: caches, MaxDepth: maxDepth})
		}
		if err == nil {
			partialScan = scan.Partial
			cleaner.ReportProgress(ctx, "Summarizing scan", 0, 0)
			data = summarize(scan)
		}
	case "status":
		var scan cleaner.Scan
		scan, err = s.LoadInventory()
		if err == nil {
			summary := summarize(scan).(scanSummary)
			summary.History, err = s.History()
			data = summary
		}
	case "history":
		data, err = s.History()
	case "reset":
		if !yes {
			err = fmt.Errorf("reset requires --yes; history and disk files are preserved")
		} else {
			err = s.ResetInventory()
			if err == nil {
				data = stopResult{Message: "Inventory reset. Disk files and cleanup history are unchanged. Run scan to start over; old plans are invalidated."}
			}
		}
	case "list":
		var scan cleaner.Scan
		scan, err = loadCandidates(s, scanID)
		if err == nil {
			items := []cleaner.Item{}
			for _, i := range scan.Items {
				if (kind == "" || i.Kind == kind) && i.Bytes >= minBytes && (ageDuration == 0 || !i.Newest.After(time.Now().Add(-ageDuration))) {
					items = append(items, i)
				}
			}
			total := len(items)
			start := min(offset, total)
			end := total
			if limit > 0 {
				end = min(total, start+limit)
			}
			var next *int
			if end < total {
				next = &end
			}
			data = itemPage{scan.ID, total, next, items[start:end], scan.Partial, scan.Inventory}
		}
	case "plan":
		var scan cleaner.Scan
		scan, err = loadCandidates(s, scanID)
		if err == nil {
			var p cleaner.Plan
			cleaner.ReportProgress(ctx, "Building cleanup plan", 0, len(scan.Items))
			p, err = cleaner.MakePlan(scan, cleaner.PlanOptions{OlderThan: ageDuration, MinBytes: minBytes, Kind: kind, Safety: safety, Limit: limit, Paths: selectedPaths})
			if err == nil {
				cleaner.ReportProgress(ctx, "Saving cleanup plan", 0, 0)
				err = s.SavePlan(p)
				data = p
			}
		}
	case "show":
		var p cleaner.Plan
		p, err = s.LoadPlan(planID)
		if err == nil {
			data = p
		}
	case "apply":
		var p cleaner.Plan
		p, err = s.LoadPlan(planID)
		if err == nil {
			p, err = cleaner.Apply(ctx, s, p, cleaner.ApplyOptions{Yes: yes, AllowReview: allowReview, AllowUnsafe: allowUnsafe}, nil)
			data = p
		}
	}
	code := "operation_failed"
	if ctx.Err() != nil {
		code = "cancelled"
	}
	cleaner.ReportProgress(ctx, "Writing results", 0, 0)
	output(out, command, data, err, code)
	if err != nil {
		return 1
	}
	return 0
}

// Run executes one command in the calling process. The menu-bar app and CLI
// share this entry point, so permissions remain with the process doing the work.
func Run(ctx context.Context, args []string, out, progressOut io.Writer) int {
	return run(ctx, args, out, progressOut)
}
