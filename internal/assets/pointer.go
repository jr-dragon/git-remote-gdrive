// Package assets implements immutable, content-addressed gdrive asset objects.
package assets

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const Version = "https://github.com/jr-dragon/git-remote-gdrive/spec/gdrive-assets/v1"
const Header = "version " + Version + "\n"
const MaxPointerSize = 512

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Pointer struct {
	OID  string
	Size int64
}

func (p Pointer) Valid() bool {
	return digestPattern.MatchString(p.OID) && p.Size >= 0 && p.Size < 1<<63-1
}
func (p Pointer) Bytes() []byte {
	return fmt.Appendf(nil, "%soid sha256:%s\nsize %d\n", Header, p.OID, p.Size)
}

// Parse distinguishes ordinary content from our reserved pointer format. Invalid
// pointers fail closed, rather than being cached as another level of indirection.
func Parse(data []byte) (Pointer, bool, error) {
	if !bytes.HasPrefix(data, []byte("version "+strings.TrimSuffix(Version, "/v1")+"/")) {
		return Pointer{}, false, nil
	}
	lines := strings.Split(string(data), "\n")
	if len(data) <= MaxPointerSize && len(lines) == 4 && lines[0]+"\n" == Header && strings.HasPrefix(lines[1], "oid sha256:") && strings.HasPrefix(lines[2], "size ") && lines[3] == "" {
		size, err := strconv.ParseInt(strings.TrimPrefix(lines[2], "size "), 10, 64)
		p := Pointer{strings.TrimPrefix(lines[1], "oid sha256:"), size}
		if err == nil && p.Valid() && bytes.Equal(p.Bytes(), data) {
			return p, true, nil
		}
	}
	return Pointer{}, true, errors.New("invalid or unsupported gdrive-assets pointer")
}
