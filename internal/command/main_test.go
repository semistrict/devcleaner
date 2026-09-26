package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestDurations(t *testing.T) {
	for _, tt := range []struct {
		s    string
		want time.Duration
	}{{"7d", 7 * 24 * time.Hour}, {"0.5d", 12 * time.Hour}, {"24h", 24 * time.Hour}, {"30m", 30 * time.Minute}} {
		got, err := duration(tt.s)
		if err != nil || got != tt.want {
			t.Fatalf("duration(%q)=%v, %v", tt.s, got, err)
		}
	}
	for _, s := range []string{"garbage", "NaNd", "999999999999999d"} {
		if _, err := duration(s); err == nil {
			t.Fatalf("invalid duration accepted: %s", s)
		}
	}
}
func TestMachineErrorsAndHelp(t *testing.T) {
	for _, args := range [][]string{{"unknown"}, {"scan", "--workers", "0"}, {"plan", "--older-than", "bad"}, {"apply"}, {"list", "--kind", "invalid"}, {"scan", "unexpected"}, {"--db"}} {
		var b bytes.Buffer
		code := run(context.Background(), append([]string{"--json"}, args...), &b, io.Discard)
		if code != 2 {
			t.Fatalf("%v: expected argument error, got %d", args, code)
		}
		var e envelope
		if err := json.Unmarshal(b.Bytes(), &e); err != nil || e.Schema != 1 || e.Error == nil || e.Error.Code != "invalid_arguments" {
			t.Fatalf("invalid machine error: %s", b.String())
		}
	}
	var b bytes.Buffer
	if run(context.Background(), []string{"--help"}, &b, io.Discard) != 0 || !bytes.Contains(b.Bytes(), []byte("Usage:")) {
		t.Fatal("help missing")
	}
}
func TestRepeatInvocationWorkflow(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "state.db")
	call := func(args ...string) map[string]any {
		t.Helper()
		args = append(args, "--db", db)
		var b bytes.Buffer
		if code := run(context.Background(), append([]string{"--json"}, args...), &b, io.Discard); code != 0 {
			t.Fatalf("%v: %s", args, b.String())
		}
		var result map[string]any
		if err := json.Unmarshal(b.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result["data"].(map[string]any)
	}
	scan := call("scan", "--root", root, "--workers", "2")
	status := call("status")
	if status["inventory"] != true || status["scan_id"] != "inventory" {
		t.Fatal("status must use persisted inventory")
	}
	historical := call("list", "--scan", scan["scan_id"].(string))
	if historical["scan_id"] != scan["scan_id"] {
		t.Fatal("historical scan must remain accessible")
	}
	list := call("list", "--limit", "1")
	if len(list["items"].([]any)) != 0 {
		t.Fatal("empty root must have empty inventory")
	}
	p := call("plan", "--older-than", "7d")
	shown := call("show", "--plan", p["id"].(string))
	if shown["id"] != p["id"] {
		t.Fatal("saved plan not available in new invocation")
	}
	var b bytes.Buffer
	code := run(context.Background(), []string{"apply", "--plan", p["id"].(string), "--db", db, "--json"}, &b, io.Discard)
	if code != 1 || !bytes.Contains(b.Bytes(), []byte("no changes made")) {
		t.Fatal("apply must require approval")
	}
}
