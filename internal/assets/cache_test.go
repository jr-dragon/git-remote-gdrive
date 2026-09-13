package assets

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestCleanAndVerifiedCache(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("binary\x00\xff\n"), bytes.Repeat([]byte("asset"), 1<<18)} {
		cache := Cache{Dir: t.TempDir()}
		var out bytes.Buffer
		if err := cache.Clean(bytes.NewReader(data), &out); err != nil {
			t.Fatal(err)
		}
		p, ok, err := Parse(out.Bytes())
		if err != nil || !ok || p.Size != int64(len(data)) {
			t.Fatalf("invalid clean pointer: %s %v", out.Bytes(), err)
		}
		var again bytes.Buffer
		if err := cache.Clean(bytes.NewReader(out.Bytes()), &again); err != nil || !bytes.Equal(again.Bytes(), out.Bytes()) {
			t.Fatal("clean is not idempotent", err)
		}
		f, err := cache.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(f)
		f.Close()
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("cache changed bytes", err)
		}
		if _, err := cache.Put(strings.NewReader("wrong"), &p); err == nil {
			t.Fatal("accepted corrupt download")
		}
		f, err = cache.Open(p)
		if err != nil {
			t.Fatal("failed download destroyed cached object", err)
		}
		f.Close()
		if err := os.WriteFile(cache.Path(p), []byte("corrupt"), 0600); err != nil {
			t.Fatal(err)
		}
		if f, err := cache.Open(p); err == nil {
			f.Close()
			t.Fatal("accepted corrupt cache")
		}
	}
}

func TestPointerValidation(t *testing.T) {
	p := Pointer{OID: strings.Repeat("a", 64), Size: 12}
	for _, data := range []string{
		strings.ReplaceAll(string(p.Bytes()), "/v1", "/v2"),
		strings.ReplaceAll(string(p.Bytes()), "size 12", "size -1"),
		strings.ReplaceAll(string(p.Bytes()), "size 12", "size 012"),
		strings.ReplaceAll(string(p.Bytes()), "size 12", "size 9223372036854775807"),
		strings.ReplaceAll(string(p.Bytes()), "aaaa", "AAAA"),
		string(p.Bytes()) + "unexpected\n",
	} {
		if _, ok, err := Parse([]byte(data)); !ok || err == nil {
			t.Fatalf("accepted malformed pointer: %q", data)
		}
	}
	if _, ok, err := Parse([]byte("version https://git-lfs.github.com/spec/v1\n")); ok || err != nil {
		t.Fatal("claimed another filter's pointer")
	}
	cache := Cache{Dir: t.TempDir()}
	if _, err := cache.Put(errorReader{}, nil); err == nil {
		t.Fatal("accepted interrupted content")
	}
	entries, err := os.ReadDir(cache.Dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("left partial cache content", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("interrupted") }
