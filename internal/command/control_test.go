package command

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devcleaner/internal/cleaner"
)

func TestStopCommandTargetsDatabaseAndRemovesSocket(t *testing.T) {
	database := filepath.Join(t.TempDir(), "state.db")
	s, err := cleaner.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeControl, err := startScanControl(database, cancel)
	if err != nil {
		t.Fatal(err)
	}
	path, err := controlPath(database, false)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.db")
	if err := requestScanStop(other); err == nil {
		t.Fatal("stop must not target a different database")
	}
	var output, progress bytes.Buffer
	// The database remains locked by the scanning process. Stop must bypass it.
	if code := run(context.Background(), []string{"stop", "--db", database}, &output, &progress); code != 0 || !strings.Contains(output.String(), "Stop requested") {
		t.Fatalf("stop failed: %s", output.String())
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("scan did not receive stop")
	}
	closeControl()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("scan control socket was not removed")
	}
	if err := requestScanStop(database); err == nil {
		t.Fatal("stop must report no active scan after cleanup")
	}
}

func TestTimeLimitSavesPartialScan(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "state.db")
	var output, progress bytes.Buffer
	code := run(context.Background(), []string{"scan", "--root", root, "--db", db, "--time-limit", "1ns", "--json"}, &output, &progress)
	if code != 0 {
		t.Fatal(output.String())
	}
	var response struct {
		Data scanSummary `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || !response.Data.Partial {
		t.Fatal("time limit must save and disclose partial results")
	}
	if !strings.Contains(progress.String(), "Stopped; partial scan saved") {
		t.Fatal(progress.String())
	}
	output.Reset()
	progress.Reset()
	if code := run(context.Background(), []string{"status", "--db", db}, &output, &progress); code != 0 || !strings.Contains(output.String(), "Partial scan") {
		t.Fatal("partial scan must be usable from a later invocation")
	}
	output.Reset()
	progress.Reset()
	if code := run(context.Background(), []string{"plan", "--db", db}, &output, &progress); code != 0 || !strings.Contains(output.String(), "partial scan") {
		t.Fatal("partial plan warning missing")
	}
}
