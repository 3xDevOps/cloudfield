package api

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ErrNotConnected means the member has saved no key.
var ErrNotConnected = errors.New("linear: no key saved")

// Dir is where the server keeps members' keys under dataDir. Nothing under
// it is mounted into a container or passed to an agent.
func Dir(dataDir string) string { return filepath.Join(dataDir, "linear") }

// Keys keeps one 0600 file per member in a 0700 directory, outside
// aether.db.
type Keys struct{ dir string }

func NewKeys(dir string) *Keys { return &Keys{dir: dir} }

func (k *Keys) path(member domain.MemberID) (string, error) {
	name := string(member)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("linear: invalid member id %q", member)
	}
	return filepath.Join(k.dir, name+".key"), nil
}

// Get returns ErrNotConnected for a member with no key.
func (k *Keys) Get(member domain.MemberID) (string, error) {
	path, err := k.path(member)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotConnected
	}
	if err != nil {
		return "", fmt.Errorf("linear: read key of member %s: %w", member, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func (k *Keys) Put(member domain.MemberID, key string) error {
	path, err := k.path(member)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(k.dir, 0o700); err != nil {
		return fmt.Errorf("linear: create directory: %w", err)
	}
	if err = os.Chmod(k.dir, 0o700); err != nil {
		return fmt.Errorf("linear: protect directory: %w", err)
	}
	// CreateTemp opens the file 0600, so the key is never readable by
	// another user, not even before the rename.
	tmp, err := os.CreateTemp(k.dir, ".write-*")
	if err != nil {
		return fmt.Errorf("linear: write key of member %s: %w", member, err)
	}
	_, err = tmp.WriteString(key + "\n")
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("linear: write key of member %s: %w", member, err)
	}
	return nil
}

// Delete returns ErrNotConnected for a member with no key.
func (k *Keys) Delete(member domain.MemberID) error {
	path, err := k.path(member)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotConnected
	}
	if err != nil {
		return fmt.Errorf("linear: delete key of member %s: %w", member, err)
	}
	return nil
}
