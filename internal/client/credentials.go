// SPDX-License-Identifier: AGPL-3.0-only

package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// Credentials is what the command line keeps: one sign-in per installation, and which
// was last signed in to (R8). It lives in a file only its owner can read, as an ssh
// key does (spec C5).
type Credentials struct {
	Current string                `json:"current,omitempty"`
	Servers map[string]Credential `json:"servers,omitempty"`
}

// Credential is what is kept of one sign-in.
type Credential struct {
	Token string `json:"token"`
}

// CredentialsPath is where the file lives, in the user's configuration folder.
func CredentialsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot find your configuration folder, where a sign-in is kept: %w", err)
	}
	return filepath.Join(dir, "allstill", "credentials.json"), nil
}

// LoadCredentials reads the file, which may not exist yet. One others can read is
// refused, as a sign-in in it may have been read already.
func LoadCredentials(path string) (Credentials, error) {
	c := Credentials{Servers: map[string]Credential{}}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	// Windows has no such modes: there, the user's profile folder is what keeps it theirs.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return c, fmt.Errorf("others can read %s, so a sign-in in it may have leaked. End it under Devices, delete the file and sign in again; or, if nobody else uses this machine, chmod 600 it", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s cannot be read. Delete it and sign in again", path)
	}
	if c.Servers == nil {
		c.Servers = map[string]Credential{}
	}
	return c, nil
}

// Save writes the file whole, readable only by its owner, so that a crash leaves
// either the old one or the new one.
func (c Credentials) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "credentials-*.json") // made readable only by its owner
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
