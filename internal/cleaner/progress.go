package cleaner

import (
	"context"
	"sync"
	"time"
)

// Progress describes one phase, not a percentage of the whole operation.
// Total is zero when the amount of work is not known yet.
type Progress struct {
	Phase     string
	Completed int
	Total     int
	Path      string
	Files     int64
	Bytes     int64
	FileTotal int64
	ByteTotal int64
	Activity  string // measured or deleted; bytes are allocated-size estimates
}

type progressKey struct{}

// WithProgress attaches a lightweight observer. Callbacks should not block;
// callers may receive updates from parallel workers.
func WithProgress(ctx context.Context, observe func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, observe)
}

func ReportProgress(ctx context.Context, phase string, completed, total int) {
	reportProgress(ctx, Progress{Phase: phase, Completed: completed, Total: total})
}

func reportProgress(ctx context.Context, state Progress) {
	if observe, ok := ctx.Value(progressKey{}).(func(Progress)); ok && observe != nil {
		observe(state)
	}
}

// Keep file callbacks cheap: the CLI renders the latest state once per second.
type fileProgress struct {
	ctx       context.Context
	state     Progress
	last      time.Time
	lastFiles int64
}

func (p *fileProgress) report(force bool) {
	if force || (p.state.Files == 1 && p.lastFiles == 0) || time.Since(p.last) >= 200*time.Millisecond {
		reportProgress(p.ctx, p.state)
		p.last = time.Now()
		p.lastFiles = p.state.Files
	}
}

func itemProgress(ctx context.Context, phase string, index, total int, item Item) context.Context {
	base := Progress{Phase: phase, Completed: index, Total: total, Path: item.Path, FileTotal: item.Files, ByteTotal: item.Bytes}
	reportProgress(ctx, base)
	return WithProgress(ctx, func(p Progress) {
		p.Phase, p.Completed, p.Total = base.Phase, base.Completed, base.Total
		p.FileTotal, p.ByteTotal = base.FileTotal, base.ByteTotal
		reportProgress(ctx, p)
	})
}

func parallelProgress[A, B any](ctx context.Context, workers int, input []A, phase string, fn func(context.Context, A) B) []B {
	ReportProgress(ctx, phase, 0, len(input))
	var mu sync.Mutex
	completed := 0
	return parallelMap(ctx, workers, input, func(ctx context.Context, job A) B {
		result := fn(ctx, job)
		mu.Lock()
		completed++
		ReportProgress(ctx, phase, completed, len(input))
		mu.Unlock()
		return result
	})
}
