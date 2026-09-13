// Package progress reports optional transport progress without using stdout.
package progress

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
)

type contextKey struct{}
type taskKey struct{}

type Reporter struct {
	mu        sync.Mutex
	out       io.Writer
	enabled   bool
	verbosity int
}

func New(ctx context.Context, out io.Writer) (context.Context, *Reporter) {
	r := &Reporter{out: out, enabled: true, verbosity: 1}
	return context.WithValue(ctx, contextKey{}, r), r
}

func (r *Reporter) SetEnabled(enabled bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled = enabled
}

func (r *Reporter) SetVerbosity(verbosity int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verbosity = verbosity
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func (r *Reporter) message(message string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.out != nil && r.enabled && r.verbosity > 0 {
		fmt.Fprintln(r.out, "gdrive: "+clean(message))
	}
}

func Step(ctx context.Context, message string) {
	r, _ := ctx.Value(contextKey{}).(*Reporter)
	if task, _ := ctx.Value(taskKey{}).(*Task); task != nil {
		message = task.label + ": " + message
	}
	r.message(message)
}

type Task struct {
	mu        sync.Mutex
	reporter  *Reporter
	label     string
	total     int
	started   int
	completed int
}

// BeginTask groups all subsequent transfers in ctx into one operation. A zero
// total keeps the task open while preparation discovers the remaining files.
func BeginTask(ctx context.Context, label string, total int) context.Context {
	if _, exists := ctx.Value(taskKey{}).(*Task); exists {
		return ctx
	}
	r, _ := ctx.Value(contextKey{}).(*Reporter)
	if r == nil {
		return ctx
	}
	t := &Task{reporter: r, label: label, total: total}
	return context.WithValue(ctx, taskKey{}, t)
}

// SetRemaining fixes an open-ended task's total after preparation transfers
// have finished. The total includes every file that has already started.
func SetRemaining(ctx context.Context, remaining int) {
	if task, _ := ctx.Value(taskKey{}).(*Task); task != nil {
		task.mu.Lock()
		task.total = task.started + max(remaining, 0)
		task.mu.Unlock()
	}
}

type Transfer struct {
	mu             sync.Mutex
	reporter       *Reporter
	task           *Task
	file           int
	label          string
	total, current int64
	last           time.Time
	finished       bool
}

func Start(ctx context.Context, label string, total int64) *Transfer {
	r, _ := ctx.Value(contextKey{}).(*Reporter)
	if r == nil {
		return nil
	}
	t := &Transfer{reporter: r, label: label, total: total, last: time.Now()}
	if task, _ := ctx.Value(taskKey{}).(*Task); task != nil {
		t.task = task
		t.file = task.start()
	}
	t.report(false, "")
	return t
}

// Update uses absolute offsets, so retries and old HTTP body readers cannot
// double-count bytes or move the display backwards. Completion is explicit.
func (t *Transfer) Update(current int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	t.current = max(t.current, min(current, t.total))
	if time.Since(t.last) >= time.Second {
		t.report(false, "")
		t.last = time.Now()
	}
}

func (t *Transfer) Finish(err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	t.finished = true
	if err != nil {
		t.report(false, ", failed")
		return
	}
	t.current = t.total
	t.report(true, ", done")
	if t.task != nil {
		t.task.complete()
	}
}

func (t *Transfer) report(done bool, suffix string) {
	filePercent := 0
	if t.total > 0 {
		filePercent = int(float64(t.current) / float64(t.total) * 100)
	}
	if done {
		filePercent = 100
	} else {
		filePercent = min(filePercent, 99)
	}
	if t.task != nil {
		t.task.report(t.file, t.label, filePercent, t.current, t.total, suffix)
		return
	}
	t.reporter.message(fmt.Sprintf("%s: %d%% (%s / %s)%s", t.label, filePercent, size(t.current), size(t.total), suffix))
}

func (t *Task) start() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.started++
	if t.total > 0 && t.started > t.total {
		t.total = t.started
	}
	return t.started
}

func (t *Task) complete() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.completed++
}

func (t *Task) report(file int, label string, filePercent int, current, total int64, suffix string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.total <= 0 {
		t.reporter.message(fmt.Sprintf("%s: file %d (%s, current file %d%%: %s / %s)%s", t.label, file, label, filePercent, size(current), size(total), suffix))
		return
	}
	position := min(max(file, 1), t.total)
	overall := (t.completed*100 + filePercent) / t.total
	if suffix == ", failed" {
		overall = t.completed * 100 / t.total
	}
	t.reporter.message(fmt.Sprintf("%s: %d%% (file %d/%d, %s, current file %d%%: %s / %s)%s", t.label, overall, position, t.total, label, filePercent, size(current), size(total), suffix))
}

func size(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	for _, unit := range []struct {
		size  int64
		label string
	}{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		if n >= unit.size {
			return fmt.Sprintf("%.1f %s", float64(n)/float64(unit.size), unit.label)
		}
	}
	return "0 B"
}

type countingReader struct {
	input    io.Reader
	transfer *Transfer
	offset   int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.input.Read(p)
	r.offset += int64(n)
	r.transfer.Update(r.offset)
	return n, err
}

func (t *Transfer) Reader(input io.Reader, offset int64) io.Reader {
	if t == nil {
		return input
	}
	return &countingReader{input: input, transfer: t, offset: offset}
}

type countingWriter struct {
	output   io.Writer
	transfer *Transfer
	offset   int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.output.Write(p)
	w.offset += int64(n)
	w.transfer.Update(w.offset)
	return n, err
}

func (t *Transfer) Writer(output io.Writer) io.Writer {
	if t == nil {
		return output
	}
	return &countingWriter{output: output, transfer: t}
}
