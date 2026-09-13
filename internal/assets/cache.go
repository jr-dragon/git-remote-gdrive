package assets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Cache struct{ Dir string }

func (c Cache) Path(p Pointer) string { return filepath.Join(c.Dir, "objects", p.OID[:2], p.OID) }

// Open verifies bytes before they can be sent to Git or uploaded to Drive.
func (c Cache) Open(p Pointer) (*os.File, error) {
	if !p.Valid() {
		return nil, errors.New("invalid asset descriptor")
	}
	f, err := os.Open(c.Path(p))
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, p.Size+1))
	if err == nil && (n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.OID) {
		err = errors.New("asset cache failed size/SHA-256 verification")
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Put streams to a private temporary file and only installs complete, verified
// content. expected is nil for clean and mandatory for downloaded content.
func (c Cache) Put(input io.Reader, expected *Pointer) (Pointer, error) {
	if expected != nil && !expected.Valid() {
		return Pointer{}, errors.New("invalid asset descriptor")
	}
	if err := os.MkdirAll(c.Dir, 0700); err != nil {
		return Pointer{}, err
	}
	f, err := os.CreateTemp(c.Dir, "incoming-*")
	if err != nil {
		return Pointer{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if expected != nil {
		input = io.LimitReader(input, expected.Size+1)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), input)
	if err != nil {
		return Pointer{}, err
	}
	p := Pointer{hex.EncodeToString(h.Sum(nil)), n}
	if expected != nil && p != *expected {
		return Pointer{}, errors.New("downloaded asset failed size/SHA-256 verification")
	}
	if !p.Valid() {
		return Pointer{}, errors.New("asset exceeds supported size")
	}
	if err := f.Sync(); err != nil {
		return Pointer{}, err
	}
	if err := f.Close(); err != nil {
		return Pointer{}, err
	}
	if err := os.MkdirAll(filepath.Dir(c.Path(p)), 0700); err != nil {
		return Pointer{}, err
	}
	if err := os.Rename(f.Name(), c.Path(p)); err != nil {
		// Windows cannot replace an open destination. A concurrent clean may
		// already have installed the same verified content.
		other, check := c.Open(p)
		if check != nil {
			return Pointer{}, fmt.Errorf("install asset cache: %w", err)
		}
		other.Close()
	}
	return p, nil
}

func (c Cache) Clean(input io.Reader, output io.Writer) error {
	prefix, err := io.ReadAll(io.LimitReader(input, MaxPointerSize+1))
	if err != nil {
		return err
	}
	_, pointer, err := Parse(prefix)
	if err != nil {
		return err
	}
	if pointer {
		_, err = output.Write(prefix)
		return err
	}
	p, err := c.Put(io.MultiReader(bytes.NewReader(prefix), input), nil)
	if err != nil {
		return err
	}
	_, err = output.Write(p.Bytes())
	return err
}
