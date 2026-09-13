package localstore

import (
	"errors"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// Path accepts absolute paths only, so Git changing directory cannot change the
// selected remote. Windows uses gdrive-local:///C:/path/to/folder.
func Path(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "gdrive-local" || u.Host != "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(u.Path, "\x00\r\n\\") {
		return "", errors.New("expected gdrive-local:///absolute/path without host, query, or fragment")
	}
	p := u.Path
	if runtime.GOOS == "windows" && len(p) >= 4 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	p = filepath.FromSlash(p)
	if !filepath.IsAbs(p) || strings.HasPrefix(p, "//") || strings.HasPrefix(p, `\\`) {
		return "", errors.New("gdrive-local requires an absolute local path (UNC paths are unsupported)")
	}
	return filepath.Clean(p), nil
}

func URL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "gdrive-local", Path: p}).String()
}
