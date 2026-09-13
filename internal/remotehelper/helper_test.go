package remotehelper

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jr-dragon/git-remote-gdrive/internal/progress"
)

func TestFolderID(t *testing.T) {
	for _, raw := range []string{"https://root", "gdrive://", "gdrive://user@root", "gdrive://root/other", "gdrive://root?x=1", "gdrive://root?", "gdrive://root#x", "gdrive://root:80", "gdrive://root%2Fother"} {
		if _, err := FolderID(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	if id, err := FolderID("gdrive://AbCd_123-xyz"); err != nil || id != "AbCd_123-xyz" {
		t.Fatal(id, err)
	}
}

func TestProgressOptions(t *testing.T) {
	var diagnostics bytes.Buffer
	ctx, reporter := progress.New(context.Background(), &diagnostics)
	h := Helper{reporter: reporter}
	for _, option := range []string{"progress false", "verbosity 0", "progress true"} {
		if got := h.option(option); got != "ok" {
			t.Fatal(got)
		}
		progress.Step(ctx, "hidden")
	}
	if diagnostics.Len() != 0 {
		t.Fatal("quiet progress was not suppressed")
	}
	if h.option("verbosity -1") != "error invalid verbosity" {
		t.Fatal("accepted invalid verbosity")
	}
	if h.option("verbosity 1") != "ok" {
		t.Fatal("verbosity rejected")
	}
	progress.Step(ctx, "visible")
	if diagnostics.String() != "gdrive: visible\n" {
		t.Fatal(diagnostics.String())
	}
}

func TestProtocolOptionsAndFraming(t *testing.T) {
	h := Helper{}
	var out bytes.Buffer
	err := h.Run(context.Background(), strings.NewReader("capabilities\noption verbosity 2\noption depth 1\noption dry-run true\noption atomic true\noption progress invalid\n\n"), &out)
	if err != nil || out.String() != "fetch\npush\noption\n\nok\nunsupported\nok\nok\nerror expected true or false\n" || !h.dryRun {
		t.Fatalf("%q: %v", out.String(), err)
	}
	for _, input := range []string{"unknown\n", "push refs/heads/a:refs/heads/a\n", "fetch abc refs/heads/a\npush refs/heads/a:refs/heads/a\n\n"} {
		if err := h.Run(context.Background(), strings.NewReader(input), &bytes.Buffer{}); err == nil {
			t.Errorf("accepted malformed stream %q", input)
		}
	}
}
