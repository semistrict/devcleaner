package command

import (
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"devcleaner/internal/cleaner"
)

// Plain periodic lines work equally well in a terminal and captured agent logs.
// A single writer owns stderr; worker callbacks only replace the latest state.
type progressReporter struct {
	mu      sync.Mutex
	state   cleaner.Progress
	stop    chan struct{}
	done    chan struct{}
	started time.Time
	command string
	out     io.Writer
}

func startProgress(out io.Writer, command string, interval time.Duration) *progressReporter {
	p := &progressReporter{state: cleaner.Progress{Phase: "Starting"}, stop: make(chan struct{}), done: make(chan struct{}), started: time.Now(), command: command, out: out}
	p.render()
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				p.render()
			}
		}
	}()
	return p
}

func (p *progressReporter) update(state cleaner.Progress) {
	p.mu.Lock()
	p.state = state
	p.mu.Unlock()
}

func (p *progressReporter) render() {
	p.mu.Lock()
	state := p.state
	p.mu.Unlock()
	detail := state.Phase
	if state.Total > 0 {
		detail += fmt.Sprintf(": %d/%d (%d%%)", state.Completed, state.Total, state.Completed*100/state.Total)
	}
	if state.Path != "" {
		detail += " — " + strconv.Quote(state.Path)
	}
	if state.Activity != "" {
		files, bytes := fmt.Sprint(state.Files), size(state.Bytes)
		if state.FileTotal > 0 {
			files += fmt.Sprintf("/%d", state.FileTotal)
		}
		if state.ByteTotal > 0 {
			bytes += "/" + size(state.ByteTotal)
		}
		detail += fmt.Sprintf(" — %s files %s, %s allocated (estimate)", files, state.Activity, bytes)
	}
	fmt.Fprintf(p.out, "[%s %s] %s\n", p.command, time.Since(p.started).Truncate(time.Second), detail)
}

func (p *progressReporter) finish(exitCode int, cancelled, partial bool) {
	close(p.stop)
	<-p.done
	state := "Completed"
	if exitCode != 0 {
		state = "Failed"
	}
	if cancelled {
		state = "Cancelled"
	}
	if partial && exitCode == 0 {
		state = "Stopped; partial scan saved"
	}
	fmt.Fprintf(p.out, "[%s %s] %s\n", p.command, time.Since(p.started).Round(time.Millisecond), state)
}
