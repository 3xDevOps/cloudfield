package memberhome

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/rootfs"
)

// The entries of an added login's directory. Credentials and Anthropic are
// what a session on the login mounts; Config and Backups are the sign-in's
// scratch, removed when it ends.
const (
	LoginCredentials = "credentials.json"
	LoginAnthropic   = "anthropic"
	LoginConfig      = ".claude.json"
	LoginBackups     = "backups"
	loginLegacyMask  = "legacy-mask"
	loginIdentity    = "identity.json"
)

// The kinds of a Claude Code login.
const (
	LoginSubscription = "subscription"
	LoginConsole      = "console"
	LoginAPIKey       = "api_key"
)

// LoginIdentity is what Claude Code recorded about a login when it signed
// in: its kind, plan, and the account and organisation it cached. It holds
// no secret.
type LoginIdentity struct {
	Kind             string `json:"kind"`
	Plan             string `json:"plan,omitempty"`
	Email            string `json:"email,omitempty"`
	OrganizationID   string `json:"organization_id,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
}

// maxIdentityField bounds each field of an identity, which comes from files
// a sign-in container wrote and reaches the owner's terminal and dashboard.
const maxIdentityField = 256

// clean drops a field that is too long or holds a control character.
func (a LoginIdentity) clean() LoginIdentity {
	for _, field := range []*string{&a.Kind, &a.Plan, &a.Email, &a.OrganizationID, &a.OrganizationName} {
		if len(*field) > maxIdentityField || !utf8.ValidString(*field) || strings.ContainsFunc(*field, unicode.IsControl) {
			*field = ""
		}
	}
	return a
}

// PerUse reports whether the login is billed per use: a Console sign-in or
// API key, which no subscription window bounds.
func (a LoginIdentity) PerUse() bool { return a.Kind == LoginConsole || a.Kind == LoginAPIKey }

// SameAccount reports whether a and b are one Anthropic account in one
// organisation, as far as both recorded it.
func (a LoginIdentity) SameAccount(b LoginIdentity) bool {
	return a.Email != "" && a.OrganizationID != "" && a.Email == b.Email && a.OrganizationID == b.OrganizationID
}

// Logins keeps members' added logins, one directory each at
// <root>/<member>/<agent>/<login>, outside every member home: mode 0700,
// files 0600, opened through roots that refuse a symlink at any component.
type Logins struct {
	root string
}

// NewLogins returns the store of added logins under root, created on first
// use.
func NewLogins(root string) (*Logins, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("memberhome: the logins root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("memberhome: logins root: %w", err)
	}
	return &Logins{root: abs}, nil
}

// Root is the directory every added login is below.
func (l *Logins) Root() string { return l.root }

func validLoginPart(kind, s string) error {
	if s == "" || len(s) > 64 {
		return fmt.Errorf("memberhome: invalid %s %q", kind, s)
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return fmt.Errorf("memberhome: invalid %s %q", kind, s)
		}
	}
	return nil
}

func (l *Logins) rel(member domain.MemberID, agent string, id domain.LoginID) (string, error) {
	if err := validateMemberID(string(member)); err != nil {
		return "", fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	if err := validLoginPart("agent", agent); err != nil {
		return "", err
	}
	if !id.Added() {
		return "", fmt.Errorf("memberhome: %q is not an added login", id)
	}
	if err := validLoginPart("login id", string(id)); err != nil {
		return "", err
	}
	return path.Join(string(member), agent, string(id)), nil
}

// Path is the directory of member's added login id of agent; it does not
// check that the directory exists.
func (l *Logins) Path(member domain.MemberID, agent string, id domain.LoginID) (string, error) {
	rel, err := l.rel(member, agent, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(l.root, filepath.FromSlash(rel)), nil
}

// Add makes the directory of a new login with an empty credentials file
// and Anthropic profile directory for a sign-in to fill. It refuses a
// directory already there.
func (l *Logins) Add(member domain.MemberID, agent string, id domain.LoginID) (string, error) {
	rel, err := l.rel(member, agent, id)
	if err != nil {
		return "", err
	}
	root, err := openManagedRoot(l.root, true)
	if err != nil {
		return "", fmt.Errorf("memberhome: open logins root: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err = ensureDir(root, path.Dir(rel)); err != nil {
		return "", fmt.Errorf("memberhome: create login directory: %w", err)
	}
	if err = root.Mkdir(rel, 0o700); err != nil {
		return "", fmt.Errorf("memberhome: create login directory: %w", err)
	}
	dir, err := rootfs.OpenRoot(root, rel)
	if err != nil {
		return "", fmt.Errorf("memberhome: create login directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := writeNew(dir, LoginCredentials, nil, 0o600); err != nil {
		return "", fmt.Errorf("memberhome: create login directory: %w", err)
	}
	if err := dir.Mkdir(LoginAnthropic, 0o700); err != nil {
		return "", fmt.Errorf("memberhome: create login directory: %w", err)
	}
	return filepath.Join(l.root, filepath.FromSlash(rel)), nil
}

// openLogin opens an existing login's directory.
func (l *Logins) openLogin(member domain.MemberID, agent string, id domain.LoginID) (*os.Root, error) {
	rel, err := l.rel(member, agent, id)
	if err != nil {
		return nil, err
	}
	root, err := openManagedRoot(l.root, false)
	if err != nil {
		return nil, fmt.Errorf("memberhome: open logins root: %w", err)
	}
	defer func() { _ = root.Close() }()
	dir, err := rootfs.OpenRoot(root, rel)
	if err != nil {
		return nil, fmt.Errorf("memberhome: open login %s: %w", id, err)
	}
	return dir, nil
}

// Remove deletes a login's directory; an absent one is a no-op.
func (l *Logins) Remove(member domain.MemberID, agent string, id domain.LoginID) error {
	rel, err := l.rel(member, agent, id)
	if err != nil {
		return err
	}
	return l.removeRel(rel)
}

// RemoveMember deletes every added login of member.
func (l *Logins) RemoveMember(member domain.MemberID) error {
	if err := validateMemberID(string(member)); err != nil {
		return fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	return l.removeRel(string(member))
}

func (l *Logins) removeRel(rel string) error {
	root, err := openManagedRoot(l.root, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memberhome: open logins root: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.RemoveAll(rel); err != nil {
		return fmt.Errorf("memberhome: remove %s: %w", rel, err)
	}
	return nil
}

// Move gives login id of agent from one member to another; it is a no-op
// when from has no such directory.
func (l *Logins) Move(from, to domain.MemberID, agent string, id domain.LoginID) error {
	src, err := l.rel(from, agent, id)
	if err != nil {
		return err
	}
	dst, err := l.rel(to, agent, id)
	if err != nil {
		return err
	}
	root, err := openManagedRoot(l.root, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memberhome: open logins root: %w", err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := ensureDir(root, path.Dir(dst)); err != nil {
		return fmt.Errorf("memberhome: move login %s: %w", id, err)
	}
	if err := root.Rename(src, dst); err != nil {
		return fmt.Errorf("memberhome: move login %s: %w", id, err)
	}
	return nil
}

// StoredLogin is one login directory on disk.
type StoredLogin struct {
	Member domain.MemberID
	Agent  string
	ID     domain.LoginID
}

// List returns every login directory, of member alone when member is set.
func (l *Logins) List(member domain.MemberID) ([]StoredLogin, error) {
	root, err := openManagedRoot(l.root, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memberhome: open logins root: %w", err)
	}
	defer func() { _ = root.Close() }()
	members := []string{string(member)}
	if member == "" {
		if members, err = dirNames(root, "."); err != nil {
			return nil, err
		}
	}
	var out []StoredLogin
	for _, m := range members {
		agents, err := dirNames(root, m)
		if err != nil {
			return nil, err
		}
		for _, agent := range agents {
			ids, err := dirNames(root, path.Join(m, agent))
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				out = append(out, StoredLogin{Member: domain.MemberID(m), Agent: agent, ID: domain.LoginID(id)})
			}
		}
	}
	return out, nil
}

func dirNames(root *os.Root, rel string) ([]string, error) {
	entries, err := fs.ReadDir(root.FS(), rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memberhome: list %s: %w", rel, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// ReadCredential returns the login's subscription sign-in file, nil when it
// is empty, under the rules of Manager.ReadCredential.
func (l *Logins) ReadCredential(member domain.MemberID, agent string, id domain.LoginID, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("memberhome: credential file limit must be positive")
	}
	dir, err := l.openLogin(member, agent, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	data, err := readRegularFile(dir, LoginCredentials, limit)
	if err != nil {
		return nil, fmt.Errorf("memberhome: read login %s: %w", id, err)
	}
	return data, nil
}

// Inspect reads what a sign-in left in the login's directory: a
// subscription sign-in, a keyless Console sign-in, or a Console API key in
// the scratch configuration. An empty Kind means the login never signed in.
func (l *Logins) Inspect(member domain.MemberID, agent string, id domain.LoginID) (LoginIdentity, error) {
	dir, err := l.openLogin(member, agent, id)
	if err != nil {
		return LoginIdentity{}, err
	}
	defer func() { _ = dir.Close() }()
	identity, err := claudeIdentity(dir, LoginCredentials, []string{LoginConfig}, LoginAnthropic)
	if err != nil {
		return LoginIdentity{}, fmt.Errorf("memberhome: read login %s: %w", id, err)
	}
	return identity, nil
}

// Settle records identity in the login's directory and removes the
// sign-in's scratch.
func (l *Logins) Settle(member domain.MemberID, agent string, id domain.LoginID, identity LoginIdentity) error {
	dir, err := l.openLogin(member, agent, id)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	if err := dir.RemoveAll(LoginConfig); err != nil {
		return fmt.Errorf("memberhome: settle login %s: %w", id, err)
	}
	if err := dir.RemoveAll(LoginBackups); err != nil {
		return fmt.Errorf("memberhome: settle login %s: %w", id, err)
	}
	if err := dir.RemoveAll(loginLegacyMask); err != nil {
		return fmt.Errorf("memberhome: settle login %s: %w", id, err)
	}
	if err := dir.RemoveAll(loginIdentity); err != nil {
		return fmt.Errorf("memberhome: settle login %s: %w", id, err)
	}
	if err := writeNew(dir, loginIdentity, data, 0o600); err != nil {
		return fmt.Errorf("memberhome: settle login %s: %w", id, err)
	}
	return nil
}

// Identity returns what Settle recorded for the login.
func (l *Logins) Identity(member domain.MemberID, agent string, id domain.LoginID) (LoginIdentity, error) {
	dir, err := l.openLogin(member, agent, id)
	if err != nil {
		return LoginIdentity{}, err
	}
	defer func() { _ = dir.Close() }()
	data, err := readRegularFile(dir, loginIdentity, maxProfileBytes)
	if err != nil {
		return LoginIdentity{}, fmt.Errorf("memberhome: read login %s: %w", id, err)
	}
	var identity LoginIdentity
	if len(data) > 0 {
		if json.Unmarshal(data, &identity) != nil {
			return LoginIdentity{}, fmt.Errorf("memberhome: read login %s: %w", id, notJSON(loginIdentity))
		}
	}
	return identity.clean(), nil
}

// DefaultIdentity is what Claude Code recorded about member's default login,
// from their home; an empty Kind when it holds none Cloudfield recognises.
func (m *Manager) DefaultIdentity(member domain.MemberID, agent string) (LoginIdentity, error) {
	if agent != "claude" {
		return LoginIdentity{}, nil
	}
	home, err := m.openExistingHome(member)
	if errors.Is(err, fs.ErrNotExist) {
		return LoginIdentity{}, nil
	}
	if err != nil {
		return LoginIdentity{}, err
	}
	defer func() { _ = home.Close() }()
	identity, err := claudeIdentity(home, claudeCredentialPath, []string{claudeLegacyConfigPath, claudeConfigPath}, AnthropicProfileDir)
	if err != nil {
		return LoginIdentity{}, fmt.Errorf("memberhome: read the default login of %q: %w", member, err)
	}
	return identity, nil
}

// AnthropicProfileDir is where Claude Code keeps a keyless Console sign-in
// in a home unless ANTHROPIC_CONFIG_DIR moves it.
const AnthropicProfileDir = anthropicProfileDir

// profileName is an Anthropic profile name Cloudfield reads; any other
// active_config content, which a container wrote, is never made a path.
var profileName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)

// maxIdentityConfigBytes bounds the configuration file read for the account
// Claude Code cached; a larger one leaves the account unknown.
const maxIdentityConfigBytes = 8 << 20

// claudeIdentity reads a Claude Code login below root: the subscription
// sign-in file oauth, the first of configs that holds anything (Claude
// Code's configuration, with the API key and the account it cached), and
// the Anthropic profile directory profile. A subscription sign-in wins over
// a Console API key, which wins over a keyless Console sign-in, as Claude
// Code itself picks them.
func claudeIdentity(root *os.Root, oauth string, configs []string, profile string) (identity LoginIdentity, err error) {
	var config struct {
		PrimaryAPIKey string `json:"primaryApiKey"`
		OAuthAccount  *struct {
			EmailAddress     string `json:"emailAddress"`
			OrganizationUUID string `json:"organizationUuid"`
			OrganizationName string `json:"organizationName"`
		} `json:"oauthAccount"`
	}
	for _, rel := range configs {
		var data []byte
		data, err = readStateUpTo(root, rel, maxIdentityConfigBytes)
		if errors.As(err, new(stateTooLarge)) {
			// Too large to read for an identity: the account it caches
			// stays unknown, as for a login signed in before this feature.
			break
		}
		if err != nil {
			return LoginIdentity{}, fmt.Errorf("%s: %w", rel, err)
		}
		if len(data) == 0 {
			continue
		}
		if json.Unmarshal(data, &config) != nil {
			return LoginIdentity{}, notJSON(rel)
		}
		break
	}
	if a := config.OAuthAccount; a != nil {
		identity.Email, identity.OrganizationID, identity.OrganizationName = a.EmailAddress, a.OrganizationUUID, a.OrganizationName
	}
	defer func() { identity = identity.clean() }()
	data, err := readLoginFile(root, oauth)
	if err != nil {
		return LoginIdentity{}, err
	}
	if len(data) > 0 {
		var file struct {
			ClaudeAIOAuth *struct {
				AccessToken      string `json:"accessToken"`
				RefreshToken     string `json:"refreshToken"`
				SubscriptionType string `json:"subscriptionType"`
			} `json:"claudeAiOauth"`
		}
		if json.Unmarshal(data, &file) != nil {
			return LoginIdentity{}, notJSON(oauth)
		}
		if o := file.ClaudeAIOAuth; o != nil && (strings.TrimSpace(o.AccessToken) != "" || strings.TrimSpace(o.RefreshToken) != "") {
			identity.Kind, identity.Plan = LoginSubscription, o.SubscriptionType
			return identity, nil
		}
	}
	if strings.TrimSpace(config.PrimaryAPIKey) != "" {
		identity.Kind = LoginAPIKey
		return identity, nil
	}
	signedIn, err := anthropicProfileSignedIn(root, profile)
	if err != nil {
		return LoginIdentity{}, err
	}
	if signedIn {
		identity.Kind = LoginConsole
	}
	return identity, nil
}

// notJSON is the error for a login file that does not parse. The decoder's
// own error can quote the file, which may hold a secret.
func notJSON(rel string) error { return fmt.Errorf("%s is not valid JSON", rel) }

func readLoginFile(root *os.Root, rel string) ([]byte, error) {
	parent, leaf, err := openLoginParent(root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	defer func() { _ = parent.Close() }()
	data, err := readRegularFile(parent, leaf, maxProfileBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	return data, nil
}

// anthropicProfileSignedIn reports whether the active Anthropic profile in
// dir is an interactive sign-in that can sign Claude Code in.
func anthropicProfileSignedIn(root *os.Root, dir string) (bool, error) {
	login, err := anthropicProfileLogin(root, dir, time.Now())
	return login != nil, err
}
