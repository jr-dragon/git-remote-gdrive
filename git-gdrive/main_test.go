package main

import (
	"bytes"
	"context"
	"testing"
)

func TestCommandValidation(t *testing.T) {
	t.Setenv("GIT_GDRIVE_CLIENT_FILE", "")
	for _, tc := range []struct {
		args []string
		fail bool
	}{
		{nil, false}, {[]string{"--help"}, false}, {[]string{"config", "--help"}, false},
		{[]string{"unknown"}, true}, {[]string{"config"}, true}, {[]string{"config", "extra"}, true},
	} {
		var output bytes.Buffer
		err := run(context.Background(), tc.args, &bytes.Buffer{}, &output)
		if (err != nil) != tc.fail {
			t.Errorf("args %v: %v", tc.args, err)
		}
	}
}
