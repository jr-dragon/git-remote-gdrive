// Package googleauth manages Google Desktop OAuth and persisted credentials.
package googleauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
)

// Credential includes the client identity needed to refresh the user's token.
type Credential struct {
	ClientID     string        `json:"client_id"`
	ClientSecret string        `json:"client_secret"`
	Token        *oauth2.Token `json:"token"`
}

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "git-remote-drive", "credential"), nil
}

func (c *Credential) validate() error {
	if c == nil || c.ClientID == "" || c.Token == nil || c.Token.AccessToken == "" || c.Token.RefreshToken == "" {
		return errors.New("incomplete credential; run git gdrive config again")
	}
	return nil
}

// Save replaces the credential atomically, keeping any prior file on failure.
func Save(path string, credential *Credential) error {
	if err := credential.validate(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("secure credential directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(credential); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func Load(path string) (*Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credential; run git gdrive config: %w", err)
	}
	var c Credential
	if json.Unmarshal(data, &c) != nil {
		return nil, errors.New("invalid credential file; run git gdrive config again")
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}
