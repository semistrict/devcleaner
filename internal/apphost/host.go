// Package apphost forwards CLI invocations to the menu-bar app's process.
// The transport is private to the logged-in user and never opens a TCP port.
package apphost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Runner func(context.Context, []string, io.Writer, io.Writer) int

// Version the endpoint when cleanup semantics change. A new CLI must never
// delegate permanent-deletion plans to an app still running the Trash build.
const socketName = "host-v2.sock"

type request struct {
	Args []string `json:"args"`
}
type response struct {
	Stream string `json:"stream,omitempty"`
	Text   []byte `json:"text,omitempty"`
	Exit   *int   `json:"exit,omitempty"`
}

func directory(create bool) (string, error) {
	path := fmt.Sprintf("/tmp/devcleaner-app-%d", os.Getuid())
	if create {
		if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return "", err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !ok || st.Uid != uint32(os.Getuid()) {
		return "", fmt.Errorf("app socket directory is not private to the current user")
	}
	return path, nil
}

type Server struct {
	listener *net.UnixListener
	lock     *os.File
	cancel   context.CancelFunc
	done     chan struct{}
}

func Listen(parent context.Context, run Runner) (*Server, error) {
	dir, err := directory(true)
	if err != nil {
		return nil, err
	}
	return listenAt(parent, dir, run)
}

func listenAt(parent context.Context, dir string, run Runner) (*Server, error) {
	lockPath := filepath.Join(dir, "host.lock")
	if info, err := os.Lstat(lockPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("app lock is a symbolic link")
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("DevCleaner is already running")
	}
	path := filepath.Join(dir, socketName)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			lock.Close()
			return nil, fmt.Errorf("unexpected file at app socket")
		}
		if err := os.Remove(path); err != nil {
			lock.Close()
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		lock.Close()
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		lock.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Server{listener: listener, lock: lock, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		var requests sync.WaitGroup
		defer requests.Wait()
		for {
			connection, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			requests.Add(1)
			go func() { defer requests.Done(); serve(ctx, connection, run) }()
		}
	}()
	return s, nil
}
func (s *Server) Close() { s.cancel(); _ = s.listener.Close(); <-s.done; _ = s.lock.Close() }

type sender struct {
	mu         sync.Mutex
	encoder    *json.Encoder
	connection net.Conn
	cancel     context.CancelFunc
}

func (s *sender) send(r response) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A stopped reader must not hold an app operation or app shutdown forever.
	_ = s.connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err := s.encoder.Encode(r)
	if err != nil {
		s.cancel()
	}
	return err
}

type streamWriter struct {
	s      *sender
	stream string
}

func (w streamWriter) Write(b []byte) (int, error) {
	original := len(b)
	for len(b) > 0 {
		n := min(len(b), 32*1024)
		if err := w.s.send(response{Stream: w.stream, Text: b[:n]}); err != nil {
			return original - len(b), err
		}
		b = b[n:]
	}
	return original, nil
}
func serve(parent context.Context, connection net.Conn, run Runner) {
	defer connection.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	send := &sender{encoder: json.NewEncoder(connection), connection: connection, cancel: cancel}
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var r request
	decoder := json.NewDecoder(io.LimitReader(connection, 64*1024))
	if err := decoder.Decode(&r); err != nil {
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	// Any subsequent input is a graceful cancellation request. Disconnects also
	// cancel work, preserving completed scan results in the app's database.
	go func() { var control map[string]bool; _ = decoder.Decode(&control); cancel() }()
	code := run(ctx, r.Args, streamWriter{send, "stdout"}, streamWriter{send, "stderr"})
	_ = send.send(response{Exit: &code})
}

// TryRun returns handled=false only when no host could be contacted. Once a
// request is sent, it must never be retried locally: it may have mutated files.
func TryRun(ctx context.Context, args []string, out, progress io.Writer) (handled bool, code int, err error) {
	dir, err := directory(false)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, err
	}
	return tryRunAt(ctx, dir, args, out, progress)
}

func tryRunAt(ctx context.Context, dir string, args []string, out, progress io.Writer) (handled bool, code int, err error) {
	connection, err := net.DialTimeout("unix", filepath.Join(dir, socketName), time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			legacy, legacyErr := net.DialTimeout("unix", filepath.Join(dir, "host.sock"), time.Second)
			if legacyErr == nil {
				legacy.Close()
				return false, 1, fmt.Errorf("the running DevCleaner app is an older build; quit and reopen the rebuilt app, or use --standalone")
			}
			return false, 0, nil
		}
		return false, 0, err
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(request{Args: args}); err != nil {
		return true, 1, err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = json.NewEncoder(connection).Encode(map[string]bool{"cancel": true})
		case <-done:
		}
	}()
	decoder := json.NewDecoder(connection)
	for {
		var r response
		if err := decoder.Decode(&r); err != nil {
			return true, 1, fmt.Errorf("lost connection to DevCleaner; inspect saved state before retrying: %w", err)
		}
		if r.Exit != nil {
			return true, *r.Exit, nil
		}
		target := out
		if r.Stream == "stderr" {
			target = progress
		}
		if _, err := target.Write(r.Text); err != nil {
			return true, 1, err
		}
	}
}
