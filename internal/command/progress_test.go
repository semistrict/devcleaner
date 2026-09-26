package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devcleaner/internal/cleaner"
)

type progressLines struct{ lines chan string }

func (w progressLines) Write(b []byte) (int, error) { w.lines <- string(b); return len(b), nil }
func receiveLine(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case line := <-ch:
		return line
	case <-time.After(time.Second):
		t.Fatal("progress stopped updating")
		return ""
	}
}
func TestProgressHeartbeatsAndCompletion(t *testing.T) {
	for _, outcome := range []struct {
		code      int
		cancelled bool
		word      string
	}{{0, false, "Completed"}, {1, false, "Failed"}, {1, true, "Cancelled"}} {
		t.Run(outcome.word, func(t *testing.T) {
			lines := make(chan string, 100)
			p := startProgress(progressLines{lines}, "scan", 10*time.Millisecond)
			if line := receiveLine(t, lines); !strings.Contains(line, "Starting") {
				t.Fatal(line)
			}
			p.update(cleaner.Progress{Phase: "Measuring directory trees", Completed: 2, Total: 5})
			// Two updates without a worker completion prove long individual jobs still
			// show activity. No terminal or cursor-control sequences are emitted.
			for range 2 {
				line := receiveLine(t, lines)
				if !strings.Contains(line, "2/5 (40%)") || strings.ContainsAny(line, "\r\x1b") {
					t.Fatal(line)
				}
			}
			p.finish(outcome.code, outcome.cancelled, false)
			last := ""
			for len(lines) > 0 {
				last = <-lines
			}
			if !strings.Contains(last, outcome.word) {
				t.Fatalf("wrong final progress: %s", last)
			}
			select {
			case <-p.done:
			default:
				t.Fatal("reporter did not stop")
			}
		})
	}
}
func TestEnglishDefaultAndSeparateProgress(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.db")
	root := t.TempDir()
	var out, progress bytes.Buffer
	code := run(context.Background(), []string{"scan", "--root", root, "--db", db}, &out, &progress)
	if code != 0 {
		t.Fatal(out.String())
	}
	if json.Valid(out.Bytes()) || !strings.Contains(out.String(), "Found 0 candidates") || !strings.Contains(out.String(), "Estimated candidate space:") {
		t.Fatalf("expected English scan results: %s", out.String())
	}
	if !strings.Contains(progress.String(), "Starting") || !strings.Contains(progress.String(), "Completed") {
		t.Fatal(progress.String())
	}
	if strings.Contains(out.String(), "[scan") {
		t.Fatal("progress leaked into results")
	}
	out.Reset()
	progress.Reset()
	if run(context.Background(), []string{"status", "--db", db, "--quiet"}, &out, &progress) != 0 || progress.Len() != 0 {
		t.Fatal("quiet must suppress all progress")
	}
	out.Reset()
	progress.Reset()
	if run(context.Background(), []string{"status", "--db", db, "--json"}, &out, &progress) != 0 || !json.Valid(out.Bytes()) {
		t.Fatal("explicit JSON must remain separate from progress")
	}
	out.Reset()
	if run(context.Background(), []string{"plan", "--db", db}, &out, io.Discard) != 0 || !strings.Contains(out.String(), "No files have been changed") {
		t.Fatal(out.String())
	}
	out.Reset()
	if run(context.Background(), []string{"apply"}, &out, io.Discard) != 2 || !strings.Contains(out.String(), "Error: --plan ID is required") {
		t.Fatal("errors must also be English by default")
	}
}
func TestEnglishPlanExplainsSafetyAndEscapesPaths(t *testing.T) {
	var out bytes.Buffer
	p := cleaner.Plan{ID: "plan-123", ScanID: "scan-123", Safety: "review", EstimatedBytes: 1024, DeletedBytes: 1024, Items: []cleaner.PlanItem{{Item: cleaner.Item{Path: "/tmp/artifact\n\x1b[31m", Kind: "ignored", Risk: "safe", Action: "delete", Bytes: 1024}, Status: "pending"}}}
	outputEnglish(&out, "plan", p, nil, "")
	for _, want := range []string{"1.0 KiB", "Permanently delete", "No files will be moved to Trash", "devcleaner apply --plan plan-123 --yes", "\\n\\x1b"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("path emitted terminal control sequence")
	}
}

func TestProgressShowsWithinItemCountsAndEscapesPath(t *testing.T) {
	var out bytes.Buffer
	p := &progressReporter{out: &out, command: "apply", started: time.Now()}
	p.update(cleaner.Progress{Phase: "Applying cleanup items", Completed: 2, Total: 10, Path: "/tmp/build\n\x1b[31m", Files: 100, FileTotal: 200, Bytes: 1024, ByteTotal: 2048, Activity: "deleted"})
	p.render()
	for _, want := range []string{"2/10 (20%)", `/tmp/build\n\x1b[31m`, "100/200 files deleted", "1.0 KiB/2.0 KiB allocated (estimate)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	if strings.Count(out.String(), "\n") != 1 || strings.ContainsAny(out.String(), "\r\x1b") {
		t.Fatal("path emitted terminal controls")
	}
}
