package apphost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDirectory(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp("/tmp", "devcleaner-host-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(path) })
	return path
}
func TestHostStreamsResultsWithoutChangingBytes(t *testing.T) {
	dir := testDirectory(t)
	text := strings.Repeat("a", 32767) + "日本語\n" + strings.Repeat("b", 40000)
	host, err := listenAt(context.Background(), dir, func(ctx context.Context, args []string, out, progress io.Writer) int {
		if len(args) != 2 || args[0] != "scan" || args[1] != "--quiet" {
			t.Error("arguments changed")
		}
		_, _ = io.WriteString(progress, "Scanning in app\n")
		_, _ = io.WriteString(out, text)
		return 7
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	var out, progress bytes.Buffer
	handled, code, err := tryRunAt(context.Background(), dir, []string{"scan", "--quiet"}, &out, &progress)
	if err != nil || !handled || code != 7 || out.String() != text || progress.String() != "Scanning in app\n" {
		t.Fatal("host did not preserve streams and exit status")
	}
	if duplicate, err := listenAt(context.Background(), dir, func(context.Context, []string, io.Writer, io.Writer) int { return 0 }); err == nil {
		duplicate.Close()
		t.Fatal("duplicate app listener must be rejected")
	}
}
func TestHostCancellationWaitsForSavedResult(t *testing.T) {
	dir := testDirectory(t)
	started := make(chan struct{})
	host, err := listenAt(context.Background(), dir, func(ctx context.Context, args []string, out, progress io.Writer) int {
		close(started)
		<-ctx.Done()
		_, _ = io.WriteString(out, "Partial scan saved.\n")
		return 0
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		var out bytes.Buffer
		handled, code, err := tryRunAt(ctx, dir, []string{"scan"}, &out, io.Discard)
		if err == nil && (!handled || code != 0 || out.String() != "Partial scan saved.\n") {
			err = io.ErrUnexpectedEOF
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("host never started")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation lost final output")
	}
	host.Close()
	if _, err := os.Stat(filepath.Join(dir, socketName)); !os.IsNotExist(err) {
		t.Fatal("host socket was not removed")
	}
}
func TestDisconnectedClientCancelsAppWork(t *testing.T) {
	dir := testDirectory(t)
	cancelled := make(chan struct{})
	host, err := listenAt(context.Background(), dir, func(ctx context.Context, args []string, out, progress io.Writer) int {
		<-ctx.Done()
		close(cancelled)
		return 0
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	connection, err := net.Dial("unix", filepath.Join(dir, socketName))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(connection).Encode(request{Args: []string{"scan"}}); err != nil {
		t.Fatal(err)
	}
	connection.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disconnected client left a scan running")
	}
}
func TestNoHostAllowsCallerToChooseFallback(t *testing.T) {
	handled, _, err := tryRunAt(context.Background(), testDirectory(t), nil, io.Discard, io.Discard)
	if handled || err != nil {
		t.Fatal("absent host must be distinguishable from an interrupted request")
	}
}

func TestOlderAppIsRejectedWithoutSendingCommand(t *testing.T) {
	dir := testDirectory(t)
	listener, err := net.Listen("unix", filepath.Join(dir, "host.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			received <- nil
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		data, _ := io.ReadAll(conn)
		received <- data
	}()
	handled, _, err := tryRunAt(context.Background(), dir, []string{"apply", "--yes"}, io.Discard, io.Discard)
	if handled || err == nil || !strings.Contains(err.Error(), "older build") {
		t.Fatal("older app must require restart")
	}
	if data := <-received; len(data) != 0 {
		t.Fatal("command was sent to an incompatible app")
	}
}
