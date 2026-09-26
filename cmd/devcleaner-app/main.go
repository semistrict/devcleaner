//go:build darwin

package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"devcleaner/internal/apphost"
	"devcleaner/internal/command"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed assets/tray.png
var trayIcon []byte

type activeCommand struct {
	kind   string
	cancel context.CancelFunc
}
type appState struct {
	mu       sync.Mutex
	active   map[uint64]activeCommand
	next     uint64
	progress string
	last     string
	quitting bool
	jobs     sync.WaitGroup
}
type captureWriter struct {
	mu    sync.Mutex
	b     bytes.Buffer
	limit int
}

func (w *captureWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(b)
	if remaining := w.limit - w.b.Len(); remaining > 0 {
		_, _ = w.b.Write(b[:min(remaining, len(b))])
	}
	return n, nil
}
func (w *captureWriter) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }

type activityWriter struct{ state *appState }

func (w activityWriter) Write(b []byte) (int, error) {
	w.state.mu.Lock()
	w.state.progress = strings.TrimSpace(string(b))
	w.state.mu.Unlock()
	return len(b), nil
}
func operation(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--db" {
			i++
			continue
		}
		switch args[i] {
		case "refresh":
			return "scan"
		case "scan", "apply", "stop", "status", "list", "plan", "show", "rules", "version":
			return args[i]
		}
	}
	return "command"
}
func (s *appState) run(ctx context.Context, args []string, out, progress io.Writer) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	if s.quitting {
		s.mu.Unlock()
		fmt.Fprintln(out, "Error: DevCleaner is quitting. Wait for it to exit before retrying.")
		return 1
	}
	s.next++
	id := s.next
	kind := operation(args)
	s.active[id] = activeCommand{kind, cancel}
	s.jobs.Add(1)
	s.mu.Unlock()
	capture := &captureWriter{limit: 12000}
	defer func() {
		s.mu.Lock()
		delete(s.active, id)
		if kind == "scan" || kind == "status" {
			s.last = capture.String()
		}
		s.mu.Unlock()
		s.jobs.Done()
	}()
	return command.Run(ctx, args, io.MultiWriter(out, capture), io.MultiWriter(progress, activityWriter{s}))
}
func (s *appState) stopScans() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, active := range s.active {
		if active.kind == "scan" {
			active.cancel()
		}
	}
}

func main() {
	// Finder launches apps with a minimal PATH. Always resolve the system Git,
	// independent of the terminal's shell setup or user-provided wrapper scripts.
	_ = os.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &appState{active: map[uint64]activeCommand{}, last: "No scan has finished in this session. Choose a folder to scan, or run the CLI with --app."}
	app := application.New(application.Options{Name: "DevCleaner", Description: "Developer disk cleanup", Mac: application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory}})
	server, err := apphost.Listen(ctx, state.run)
	if err != nil {
		log.Print(err)
		return
	}
	app.OnShutdown(func() { cancel(); server.Close() })
	tray := app.SystemTray.New()
	tray.SetTemplateIcon(trayIcon)
	tray.SetTooltip("DevCleaner")
	menu := app.Menu.New()
	status := menu.Add("Ready — CLI requests run in this app").SetEnabled(false)
	menu.AddSeparator()
	showMessage := func(title, message string) { app.Dialog.Info().SetTitle(title).SetMessage(message).Show() }
	startScan := func(root string) {
		go func() {
			var result bytes.Buffer
			code := state.run(ctx, []string{"scan", "--root", root}, &result, io.Discard)
			if code != 0 {
				showMessage("Scan could not finish", result.String())
			}
		}()
	}
	home, _ := os.UserHomeDir()
	menu.Add("Scan Home Directory").OnClick(func(*application.Context) { startScan(home) })
	menu.Add("Choose Folder to Scan…").OnClick(func(*application.Context) {
		root, err := app.Dialog.OpenFile().SetTitle("Choose a developer workspace").CanChooseDirectories(true).CanChooseFiles(false).PromptForSingleSelection()
		if err != nil {
			showMessage("Could not choose folder", err.Error())
			return
		}
		if root != "" {
			startScan(root)
		}
	})
	stopItem := menu.Add("Stop Scan and Keep Results").OnClick(func(*application.Context) { state.stopScans() }).SetEnabled(false)
	menu.Add("Last Scan Result…").OnClick(func(*application.Context) {
		state.mu.Lock()
		last := state.last
		progress := state.progress
		busy := len(state.active) > 0
		state.mu.Unlock()
		if busy {
			last = "Current activity: " + progress + "\n\n" + last
		}
		showMessage("DevCleaner", last)
	})
	menu.AddSeparator()
	menu.Add("Disk Access Settings…").OnClick(func(*application.Context) {
		if err := app.Browser.OpenURL("x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"); err != nil {
			showMessage("Disk access", "Open System Settings → Privacy & Security → Full Disk Access and add DevCleaner.app.")
		}
	})
	menu.Add("Reveal DevCleaner.app").OnClick(func(*application.Context) {
		executable, err := os.Executable()
		if err != nil {
			showMessage("Could not locate app", err.Error())
			return
		}
		bundle := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
		if err := exec.Command("/usr/bin/open", "-R", bundle).Run(); err != nil {
			showMessage("Could not reveal app", err.Error())
		}
	})
	menu.Add("CLI and Permissions…").OnClick(func(*application.Context) {
		showMessage("Use DevCleaner from your terminal", "Keep this app running and use:\n\ndevcleaner scan --app\ndevcleaner stop --app\n\nThe Go scanner runs inside DevCleaner.app. Grant disk access to this app in System Settings, not to iTerm. You must enable Full Disk Access yourself if needed. Quit and reopen the app after changing that permission.\n\nWithout --app, the CLI automatically uses this app when available. --app refuses to fall back to terminal permissions. --standalone runs directly in the terminal.\n\nCleanup still requires a saved plan and explicit approval flags. The menu never starts cleanup automatically.")
	})
	menu.AddSeparator()
	menu.Add("Quit DevCleaner").OnClick(func(*application.Context) {
		state.mu.Lock()
		if state.quitting {
			state.mu.Unlock()
			return
		}
		state.quitting = true
		for _, active := range state.active {
			active.cancel()
		}
		state.mu.Unlock()
		status.SetLabel("Finishing active work before quitting…")
		menu.Update()
		go func() { state.jobs.Wait(); app.Quit() }()
	})
	tray.SetMenu(menu)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				state.mu.Lock()
				count := len(state.active)
				label := state.progress
				scanning := false
				quitting := state.quitting
				for _, active := range state.active {
					if active.kind == "scan" {
						scanning = true
					}
				}
				state.mu.Unlock()
				if quitting {
					continue
				}
				if count == 0 {
					label = "Ready — CLI requests run in this app"
				}
				if len(label) > 110 {
					label = label[:110] + "…"
				}
				application.InvokeAsync(func() { status.SetLabel(label); stopItem.SetEnabled(scanning); menu.Update() })
			}
		}
	}()
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
