package command

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type stopResult struct {
	Message string `json:"message"`
}

// A private, database-specific local socket lets another invocation request a
// graceful stop without taking SQLite's lock or signalling an unrelated PID.
// Keep the socket path short enough for macOS even with a long database path.
func controlPath(database string, create bool) (string, error) {
	absolute, err := filepath.Abs(database)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(filepath.Join(parent, filepath.Base(absolute))))
	directory := fmt.Sprintf("/tmp/devcleaner-control-%d", os.Getuid())
	if create {
		if err := os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
			return "", err
		}
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Getuid()) {
		return "", fmt.Errorf("scan control directory must be private and owned by the current user")
	}
	return filepath.Join(directory, fmt.Sprintf("%x.sock", key[:16])), nil
}

// Must be called while holding the database lock. That lock proves an old
// socket for this database is stale before replacing it.
func startScanControl(database string, cancel context.CancelFunc) (func(), error) {
	path, err := controlPath(database, true)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("unexpected file at scan control socket")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			request := make([]byte, 5)
			if _, err := io.ReadFull(connection, request); err == nil && string(request) == "stop\n" {
				cancel()
				_, _ = io.WriteString(connection, "stopping\n")
			}
			_ = connection.Close()
		}
	}()
	return func() { _ = listener.Close(); <-done }, nil
}

func requestScanStop(database string) error {
	path, err := controlPath(database, false)
	if err != nil {
		return fmt.Errorf("no active scan found for this database: %w", err)
	}
	connection, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return fmt.Errorf("no active scan found for this database: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = io.WriteString(connection, "stop\n"); err != nil {
		return err
	}
	reply := make([]byte, 9)
	if _, err = io.ReadFull(connection, reply); err != nil {
		return err
	}
	if string(reply) != "stopping\n" {
		return fmt.Errorf("unexpected scan control response")
	}
	return nil
}
