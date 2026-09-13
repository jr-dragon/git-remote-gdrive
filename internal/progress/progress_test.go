package progress

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTransferOffsetsAndCompletion(t *testing.T) {
	var out bytes.Buffer
	ctx, _ := New(context.Background(), &out)
	transfer := Start(ctx, "Uploading pack", 100)
	_, _ = io.Copy(io.Discard, transfer.Reader(strings.NewReader(strings.Repeat("a", 60)), 0))
	// A resumed request starts at its acknowledged offset, rather than adding
	// resent bytes to the total. Late readers must not move progress backwards.
	_, _ = io.Copy(io.Discard, transfer.Reader(strings.NewReader(strings.Repeat("a", 50)), 30))
	transfer.Update(20)
	if transfer.current != 80 {
		t.Fatalf("retry double-counted bytes: %d", transfer.current)
	}
	if strings.Count(out.String(), "gdrive:") != 1 {
		t.Fatal("rapid reads flooded progress output")
	}
	transfer.last = time.Now().Add(-2 * time.Second)
	transfer.Update(100)
	if !strings.Contains(out.String(), "99%") || strings.Contains(out.String(), "100%") {
		t.Fatal("reported completion before confirmation", out.String())
	}
	transfer.Finish(nil)
	before := out.String()
	transfer.Update(100)
	transfer.Finish(nil)
	if out.String() != before || !strings.Contains(before, "100% (100 B / 100 B), done") {
		t.Fatal(out.String())
	}
}

func TestTaskAggregatesFiles(t *testing.T) {
	var out bytes.Buffer
	ctx, _ := New(context.Background(), &out)
	ctx = BeginTask(ctx, "Push", 2)
	first := Start(ctx, "Uploading pack", 100)
	first.Update(50)
	first.Finish(nil)
	second := Start(ctx, "Uploading manifest", 20)
	second.Finish(nil)
	got := out.String()
	for _, want := range []string{
		"Push: 0% (file 1/2, Uploading pack",
		"Push: 50% (file 1/2, Uploading pack, current file 100%",
		"Push: 50% (file 2/2, Uploading manifest, current file 0%",
		"Push: 100% (file 2/2, Uploading manifest, current file 100%",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

func TestTaskPlansAfterPreparation(t *testing.T) {
	var out bytes.Buffer
	ctx, _ := New(context.Background(), &out)
	ctx = BeginTask(ctx, "Push", 0)
	first := Start(ctx, "Receiving prerequisite", 10)
	first.Finish(nil)
	SetRemaining(ctx, 2)
	Step(ctx, "Processing 3 files...")
	second := Start(ctx, "Uploading asset", 10)
	second.Finish(errors.New("failed"))
	third := Start(ctx, "Uploading pack", 10)
	third.Finish(nil)
	got := out.String()
	for _, want := range []string{
		"Push: file 1 (Receiving prerequisite",
		"Push: Processing 3 files",
		"Push: 33% (file 2/3, Uploading asset",
		"Push: 66% (file 3/3, Uploading pack",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Push: 100%") {
		t.Fatal("failed file was counted as complete", got)
	}
}

func TestFailedAndEmptyTransfers(t *testing.T) {
	var out bytes.Buffer
	ctx, _ := New(context.Background(), &out)
	transfer := Start(ctx, "Receiving asset", 5)
	writer := transfer.Writer(io.Discard)
	_, _ = writer.Write([]byte("hello"))
	transfer.Finish(errors.New("invalid checksum"))
	if strings.Contains(out.String(), "100%") || !strings.Contains(out.String(), "failed") {
		t.Fatal("unverified data reported successful", out.String())
	}
	out.Reset()
	Start(ctx, "Empty asset", 0).Finish(nil)
	if !strings.Contains(out.String(), "100% (0 B / 0 B), done") {
		t.Fatal(out.String())
	}
}

func TestQuietSanitizationAndConcurrentReaders(t *testing.T) {
	var out bytes.Buffer
	ctx, reporter := New(context.Background(), &out)
	reporter.SetEnabled(false)
	Step(ctx, "hidden")
	Start(ctx, "hidden", 1).Finish(nil)
	reporter.SetEnabled(true)
	reporter.SetVerbosity(0)
	Step(ctx, "hidden")
	if out.Len() != 0 {
		t.Fatal("quiet progress emitted output")
	}
	reporter.SetVerbosity(1)
	transfer := Start(ctx, "name\n\x1b[31m", 100)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 100 {
				transfer.Update(int64(i + j))
			}
		})
	}
	wg.Wait()
	transfer.Finish(nil)
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "name\n") {
		t.Fatal("unsafe terminal label", out.String())
	}
	// No reporter is a no-op, including stream wrappers and completion.
	none := Start(context.Background(), "hidden", 0)
	none.Finish(nil)
	if none.Reader(strings.NewReader("test"), 0) == nil || none.Writer(io.Discard) == nil {
		t.Fatal("nil progress broke IO")
	}
}
