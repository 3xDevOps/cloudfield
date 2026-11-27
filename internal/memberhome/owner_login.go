package memberhome

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

const (
	claudeConfigPath       = ".claude.json"
	claudeLegacyConfigPath = ".claude/.config.json"
	anthropicProfileDir    = ".config/anthropic"
	maxProfileBytes        = 1 << 20
	// A profile is a file name; NAME_MAX is 255 with the ".json" suffix.
	maxProfileName = 250
)

// OwnerLogin is a login an agent keeps outside its login file. It signs in
// the owner's own sessions, which mount the whole home. An account share
// mounts only the login file, so it never reaches a borrower.
type OwnerLogin struct {
	// File is the home-relative file or directory that holds it.
	File string
	// Stamp changes when the login does, and not when the agent rewrites
	// the rest of File.
	Stamp string
}

// OwnerLogin returns the OwnerLogin of agent in member's home, or nil when
// there is none. Claude Code has two, both Anthropic Console sign-ins: the
// API key it created, under primaryApiKey in its configuration file, and the
// Anthropic profile a sign-in without a key writes below ~/.config/anthropic.
// A file that is a symlink, of another type, with another hard link, too
// large or not JSON is an error. A profile directory behind a symlink is
// not read and is no login.
func (m *Manager) OwnerLogin(member domain.MemberID, agent string) (*OwnerLogin, error) {
	if agent != "claude" {
		return nil, nil
	}
	home, err := m.openExistingHome(member)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = home.Close() }()
	login, err := m.claudeAPIKeyLogin(member, home)
	if login == nil {
		var profileErr error
		login, profileErr = anthropicProfileLogin(home, anthropicProfileDir, time.Now())
		err = cmp.Or(err, profileErr)
	}
	if login == nil && err != nil {
		return nil, fmt.Errorf("memberhome: read %s login of %q: %w", agent, member, err)
	}
	return login, nil
}

// apiKeyLoginRead is what claudeAPIKeyLogin found in the file that was rel,
// size and modified then.
type apiKeyLoginRead struct {
	rel      string
	size     int64
	modified time.Time
	login    *OwnerLogin
}

// claudeAPIKeyLogin reads the configuration file Claude Code reads: the
// legacy one when it holds anything, ~/.claude.json otherwise. The file
// grows with every project Claude Code has opened and every agent.list of a
// member without a login file asks, so an unchanged file is not read again.
func (m *Manager) claudeAPIKeyLogin(member domain.MemberID, home *os.Root) (*OwnerLogin, error) {
	for _, rel := range []string{claudeLegacyConfigPath, claudeConfigPath} {
		info, err := home.Lstat(rel)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || (err == nil && info.Mode().IsRegular() && info.Size() == 0) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		m.mu.Lock()
		cached := m.apiKeyLogins[member]
		m.mu.Unlock()
		// home.Lstat follows a symlinked directory inside the home, which
		// the readers refuse, so the cached read is checked without one.
		if pinned, pinErr := lstatLogin(home, rel); pinErr == nil && pinned.Mode().IsRegular() && !hasMultipleLinks(pinned) &&
			cached.rel == rel && cached.size == pinned.Size() && cached.modified.Equal(pinned.ModTime()) {
			return cached.login, nil
		}
		data, err := readState(home, rel)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		var config struct {
			PrimaryAPIKey string `json:"primaryApiKey"`
		}
		if len(data) > 0 {
			if err = json.Unmarshal(data, &config); err != nil {
				return nil, fmt.Errorf("%s: %w", rel, err)
			}
		}
		read := apiKeyLoginRead{rel: rel, size: info.Size(), modified: info.ModTime()}
		if key := strings.TrimSpace(config.PrimaryAPIKey); key != "" {
			read.login = &OwnerLogin{File: rel, Stamp: loginDigest(rel, key)}
		}
		m.mu.Lock()
		if m.apiKeyLogins == nil {
			m.apiKeyLogins = make(map[domain.MemberID]apiKeyLoginRead)
		}
		m.apiKeyLogins[member] = read
		m.mu.Unlock()
		return read.login, nil
	}
	return nil, nil
}

// anthropicProfileLogin reads the active Anthropic profile as Claude Code
// does: the one active_config names, or "default". Only a profile an
// interactive sign-in wrote is a login here; a federation profile holds no
// credential of its own, and an expired token without a refresh token signs
// nothing in.
func anthropicProfileLogin(home *os.Root, dir string, now time.Time) (*OwnerLogin, error) {
	// A profile directory the server will not read through, such as a
	// symlinked ~/.config, is none it can vouch for, and no error either:
	// most such homes hold no profile at all.
	if info, err := lstatLogin(home, dir); err != nil || !info.IsDir() {
		return nil, nil
	}
	active, err := readProfileFile(home, dir, "active_config", maxProfileName+2)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(string(active))
	if name == "" {
		name = "default"
	}
	if name != path.Base(name) || name == "." || name == ".." || len(name) > maxProfileName {
		return nil, nil
	}
	config, err := readProfileFile(home, dir, "configs/"+name+".json", maxProfileBytes)
	if err != nil || len(config) == 0 {
		return nil, err
	}
	var profile struct {
		Authentication struct {
			Type string `json:"type"`
		} `json:"authentication"`
	}
	if json.Unmarshal(config, &profile) != nil {
		return nil, notJSON(dir + "/configs/<active profile>.json")
	}
	if profile.Authentication.Type != "user_oauth" {
		return nil, nil
	}
	rel := "credentials/" + name + ".json"
	credentials, err := readProfileFile(home, dir, rel, maxProfileBytes)
	if err != nil {
		return nil, err
	}
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		// Claude Code writes seconds since the epoch; any other form is
		// not judged.
		ExpiresAt any `json:"expires_at"`
	}
	if len(credentials) == 0 {
		return nil, nil
	}
	if json.Unmarshal(credentials, &tokens) != nil {
		return nil, notJSON(dir + "/credentials/<active profile>.json")
	}
	renews := strings.TrimSpace(tokens.RefreshToken) != ""
	expiresAt, _ := tokens.ExpiresAt.(float64)
	expired := expiresAt > 0 && float64(now.Unix()) >= expiresAt
	if !renews && (strings.TrimSpace(tokens.AccessToken) == "" || expired) {
		return nil, nil
	}
	return &OwnerLogin{File: dir, Stamp: loginDigest(rel, string(credentials))}, nil
}

// lstatLogin stats rel without following a symlink at any component.
func lstatLogin(home *os.Root, rel string) (fs.FileInfo, error) {
	parent, leaf, err := openLoginParent(home, rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	return parent.Lstat(leaf)
}

// readProfileFile reads rel in the profile directory dir. Its errors name
// the file only by its place, since a profile's name comes from a file.
func readProfileFile(home *os.Root, dir, rel string, limit int64) ([]byte, error) {
	where := dir + "/" + strings.SplitN(rel, "/", 2)[0]
	parent, leaf, err := openLoginParent(home, dir+"/"+rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, withoutPath(err))
	}
	defer func() { _ = parent.Close() }()
	data, err := readRegularFile(parent, leaf, limit)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, withoutPath(err))
	}
	return data, nil
}

// withoutPath is err without the file name it carries, which can hold a
// profile name read from a file.
func withoutPath(err error) error {
	if pathErr := (*fs.PathError)(nil); errors.As(err, &pathErr) {
		return fmt.Errorf("%s: %w", pathErr.Op, pathErr.Err)
	}
	return errors.New("it is not a single regular file within the size limit")
}

func loginDigest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}
