package remotehelper

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
