package edgeagent

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// StateDir is where the agent keeps its state under a server data
// directory. Every directory is 0700 and every file 0600, and a root
// process gives what it creates to the data directory's owner.
func StateDir(dataDir string) string { return filepath.Join(dataDir, "edge") }

const (
	pinFile    = "edge_key.json"
	ownerFile  = "owner.json"
	claimFile  = "claim.json"
	statusFile = "status.json"
	lockFile   = "lock"
	// keysDir holds one directory per edge key the server has pinned,
	// with the owner it has at the edge holding that key.
	keysDir = "keys"
	// edgeStateFile, beside ownerFile, records the state the edge holding
	// the key last reported for this server.
	edgeStateFile = "edge_state.json"
	// noticesFile, beside ownerFile, holds the verified notices of the
	// latest enrollment at the edge holding the key; noticesPinFile, a
	// hard link to the pin file they were stored under, binds them to
	// that pin.
	noticesFile    = "notices.json"
	noticesPinFile = "notices.pin"
)

// State is the agent's persistent state: the pinned edge key, the owner,
// the claim code and the agent's last status. The serve process and the
// aether-server edge commands share it, so every method reads the files
// afresh.
//
// Trust in an edge rests on its signing key, not on a host name, so none
// of it depends on edge-url: a server moved to another host name of the
// same edge keeps its pin and owner. The owner is kept per edge key: an
// edge with another key, once `aether-server edge trust` pins it, starts
// without one, and pinning the first edge's key again finds its owner.
type State struct {
	dir string
	// beforeLink and beforePublish, when set, run just before
	// writeNotices links the pin file and pinFirst publishes one; tests
	// pin a key there as an older binary, which takes no lock, would.
	beforeLink, beforePublish func()
	// linkFile and renameNoReplace replace os.Link and the rename that
	// replaces nothing when set; tests make them fail as a file system
	// without them does.
	linkFile, renameNoReplace func(oldname, newname string) error
}

// OpenState returns the state under dataDir. Nothing is created until a
// method writes.
func OpenState(dataDir string) *State { return &State{dir: StateDir(dataDir)} }

// Status is what the agent last reported about its control connection.
// While Connected, PID is the server process and Heartbeat the last time
// the edge answered: its Ready at enrollment, then each Pong.
type Status struct {
	Edge      string    `json:"edge"`
	Connected bool      `json:"connected"`
	Error     string    `json:"error,omitempty"`
	Since     time.Time `json:"since"`
	PID       int       `json:"pid,omitempty"`
	Heartbeat time.Time `json:"heartbeat,omitzero"`
	// Protocol is the edge protocol version of the connection.
	Protocol int `json:"protocol,omitempty"`
}

// HeartbeatMaxAge is how old the heartbeat of a live control connection
// gets: the agent pings every edgeproto.PingInterval, so an older one
// means two pings went unanswered.
const HeartbeatMaxAge = 2 * edgeproto.PingInterval

// Stale is why a Connected st no longer holds at now: the server process
// it names is gone, which a stop without the agent's clean shutdown
// leaves, or no ping was answered within HeartbeatMaxAge, as when the
// server hangs or the edge dropped the connection unnoticed. Nil while it
// holds, and for a status without a Heartbeat, which a server older than
// heartbeats wrote and nothing here can check.
func (st Status) Stale(now time.Time) error {
	if !st.Connected {
		return nil
	}
	if st.PID > 0 && !processRunning(st.PID) {
		return fmt.Errorf("server process %d is not running; it stopped without closing the connection", st.PID)
	}
	if st.Heartbeat.IsZero() {
		return nil
	}
	if age := now.Sub(st.Heartbeat); age > HeartbeatMaxAge {
		return fmt.Errorf("no answer from the edge for %s (limit %s); the server is hung or the connection is dead and not yet detected",
			age.Round(time.Second), HeartbeatMaxAge)
	}
	return nil
}

// processRunning reports whether pid is a live process. A process this
// one may not signal is live.
func processRunning(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return !errors.Is(p.Signal(syscall.Signal(0)), os.ErrProcessDone)
}

// ClaimCode describes the current claim code. The code itself is shown
// only when it is issued: the state keeps its hash. Admin, when set, is
// the existing admin member the claim binds the claiming account to.
type ClaimCode struct {
	Hash         string    `json:"hash"`
	ExpiresAt    time.Time `json:"expires_at"`
	AttemptsLeft int       `json:"attempts_left"`
	Admin        string    `json:"admin,omitempty"`
}

// Usable reports whether the code can still claim the server at now.
func (c ClaimCode) Usable(now time.Time) bool {
	return c.Hash != "" && c.AttemptsLeft > 0 && !now.After(c.ExpiresAt)
}

// pin is the pinned edge key. file is the pin file it was read from:
// every pin, by this build or an older one, puts a new file in place, so a
// later pin, even of the same key, is another file. f, when set, holds
// that file open, which keeps its file number from being reused; close
// it with close.
type pin struct {
	Key  ed25519.PublicKey `json:"key"`
	file os.FileInfo
	f    *os.File
}

func (p pin) close() {
	if p.f != nil {
		_ = p.f.Close()
	}
}

// openPin opens the pin file at path and returns its pin, holding the
// file open, or a zero pin when there is none.
func openPin(path string) (pin, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return pin{}, nil
	}
	if err != nil {
		return pin{}, fmt.Errorf("edgeagent: %w", err)
	}
	p, err := readPin(f)
	if err != nil {
		_ = f.Close()
		return pin{}, err
	}
	return p, nil
}

// readPin reads the pin in f, which the pin keeps.
func readPin(f *os.File) (pin, error) {
	info, err := f.Stat()
	if err != nil {
		return pin{}, fmt.Errorf("edgeagent: %w", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return pin{}, fmt.Errorf("edgeagent: read %s: %w", f.Name(), err)
	}
	var p pin
	if err := json.Unmarshal(data, &p); err != nil {
		return pin{}, fmt.Errorf("edgeagent: parse %s: %w", f.Name(), err)
	}
	if len(p.Key) != ed25519.PublicKeySize {
		return pin{}, fmt.Errorf("edgeagent: %s holds a %d-byte key, want %d", f.Name(), len(p.Key), ed25519.PublicKeySize)
	}
	p.file, p.f = info, f
	return p, nil
}

// pinned returns the pin, with a nil Key when none is pinned, and does
// not hold its file.
func (s *State) pinned() (pin, error) {
	p, err := openPin(s.path(pinFile))
	p.close()
	p.f = nil
	return p, err
}

// PinnedKey returns the pinned edge key, nil when none is pinned.
func (s *State) PinnedKey() (ed25519.PublicKey, error) {
	p, err := s.pinned()
	return p.Key, err
}

// Pin replaces the pinned edge key. It deletes the notices stored for the
// key it replaces and for key, so none survives a change of pin.
func (s *State) Pin(key ed25519.PublicKey) error {
	if len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("edgeagent: edge key is %d bytes, want %d", len(key), ed25519.PublicKeySize)
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := s.pinned()
	if err != nil {
		return err
	}
	if err := s.forgetNotices(cur.Key, key); err != nil {
		return err
	}
	return s.write(pinFile, pin{Key: key})
}

func (s *State) noReplace(oldname, newname string) error {
	if s.renameNoReplace != nil {
		return s.renameNoReplace(oldname, newname)
	}
	return renameNoReplace(oldname, newname)
}

// unsupported reports whether err says the file system or the kernel
// does not offer the operation.
func unsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EOPNOTSUPP)
}

// noReplaceUnavailable reports whether err says the rename that
// replaces nothing cannot be used here: unsupported, or refused by a
// system call policy such as seccomp, which answers EPERM or EACCES.
func noReplaceUnavailable(err error) bool {
	return unsupported(err) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

// cannotLink reports whether err says this file system or policy makes
// no hard link here: unsupported (ENOSYS, ENOTSUP, EOPNOTSUPP), EPERM
// (vfat, or protected hard links), EACCES (a container policy), EXDEV and
// EMLINK. Any other error, such as EIO, ENOSPC or EDQUOT, is a failure.
func cannotLink(err error) bool {
	if errors.Is(err, errors.ErrUnsupported) {
		return true
	}
	for _, e := range []syscall.Errno{syscall.EPERM, syscall.EACCES, syscall.EXDEV, syscall.EMLINK} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// noticesTemp matches the names writeNotices' temporary files take: the
// notices file's name with a generated number, and its link with .pin.
var noticesTemp = regexp.MustCompile(`^\.` + regexp.QuoteMeta(noticesFile) + `\.[0-9]+(\.pin)?$`)

func (s *State) link(oldname, newname string) error {
	if s.linkFile != nil {
		return s.linkFile(oldname, newname)
	}
	return os.Link(oldname, newname)
}

// sameRegular reports whether path itself, not a file a symbolic link
// there names, is the regular file held.
func sameRegular(path string, held os.FileInfo) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && os.SameFile(info, held)
}

// forgetNotices deletes the stored notices of each key.
func (s *State) forgetNotices(keys ...ed25519.PublicKey) error {
	for _, k := range keys {
		if k == nil {
			continue
		}
		for _, file := range []string{noticesPinFile, noticesFile} {
			if err := s.remove(keyDirFile(k, file)); err != nil {
				return err
			}
		}
	}
	return nil
}

// pinFirst returns the pin of key, holding its file open: the one it
// pinned, and true, when none was pinned, or the one already pinned, and
// false. It publishes a pin only where none is, so a pin an older binary,
// which takes no lock, wrote meanwhile is neither overwritten nor taken
// for this one; another key is refused as a changed edge key.
func (s *State) pinFirst(key ed25519.PublicKey) (pin, bool, error) {
	unlock, err := s.lock()
	if err != nil {
		return pin{}, false, err
	}
	defer unlock()
	cur, err := openPin(s.path(pinFile))
	if err != nil {
		return pin{}, false, err
	}
	created := false
	if cur.Key == nil {
		if err = s.forgetNotices(key); err != nil {
			return pin{}, false, err
		}
		if cur, created, err = s.publishPin(key); err != nil {
			return pin{}, false, err
		}
	}
	if !cur.Key.Equal(key) {
		cur.close()
		return pin{}, false, pinMismatch(cur.Key, key)
	}
	return cur, created, nil
}

// publishPin puts a pin of key in place unless a pin file appeared, and
// returns the pin file in place, held open, and whether it is this one.
func (s *State) publishPin(key ed25519.PublicKey) (pin, bool, error) {
	tmp, err := s.writeTemp(pinFile, pin{Key: key})
	if err != nil {
		return pin{}, false, err
	}
	defer os.Remove(tmp) //nolint:errcheck // a second name of the pin, or unused
	f, err := os.Open(tmp)
	if err != nil {
		return pin{}, false, fmt.Errorf("edgeagent: %w", err)
	}
	if s.beforePublish != nil {
		s.beforePublish()
	}
	// Put the pin in place only where none is: a rename that replaces
	// nothing, else a hard link. A file system with neither pins by a
	// plain rename, as builds before notices did, which can replace a pin
	// another process wrote in the same instant.
	err = s.noReplace(tmp, s.path(pinFile))
	if noReplaceUnavailable(err) {
		if err = s.link(tmp, s.path(pinFile)); cannotLink(err) {
			// The rename replaces whatever is there, such as a symbolic
			// link to a missing file the read above took for no pin.
			switch _, lerr := os.Lstat(s.path(pinFile)); {
			case lerr == nil:
				err = fmt.Errorf("edgeagent: %s: %w", s.path(pinFile), fs.ErrExist)
			case errors.Is(lerr, fs.ErrNotExist):
				err = os.Rename(tmp, s.path(pinFile))
			default:
				err = lerr
			}
		}
	}
	if errors.Is(err, fs.ErrExist) {
		_ = f.Close()
		p, oerr := openPin(s.path(pinFile))
		if oerr == nil && p.Key == nil {
			oerr = fmt.Errorf("edgeagent: %s exists but names no readable pin file, as a symbolic link to a missing file does; "+
				"pin the edge's key with `aether-server edge trust`", s.path(pinFile))
		}
		return p, false, oerr
	}
	if err != nil {
		_ = f.Close()
		return pin{}, false, fmt.Errorf("edgeagent: %w", err)
	}
	p, err := readPin(f)
	if err != nil {
		_ = f.Close()
		return pin{}, false, err
	}
	return p, true, nil
}

// refuseEarlierPins refuses a first pin while an edge key pinned by a
// development build that kept a directory per relay origin is present:
// pinning over it would trust whatever key the edge presents.
func (s *State) refuseEarlierPins() error {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	var earlier, dirs []string
	for _, e := range entries {
		p := filepath.Join(s.dir, e.Name(), pinFile)
		if _, err := os.Lstat(p); err == nil && e.IsDir() {
			earlier, dirs = append(earlier, p), append(dirs, filepath.Dir(p))
		}
	}
	if len(earlier) == 0 {
		return nil
	}
	return fmt.Errorf("edgeagent: %s: an edge key pinned by an earlier development build, which this server does not read; "+
		"pin the edge's key with `sudo aether-server edge trust`, or remove the old state with `sudo rm -r %s` to pin the key the edge presents at the next connection",
		strings.Join(earlier, ", "), strings.Join(dirs, " "))
}

// Owner returns the account that owns this server at the edge holding
// the pinned key, by a claim or a transfer, nil when it has none or no
// key is pinned.
func (s *State) Owner() (*edgeproto.Account, error) {
	name, err := s.ownerName()
	if err != nil || name == "" {
		return nil, err
	}
	var a edgeproto.Account
	ok, err := s.read(name, &a)
	if err != nil || !ok {
		return nil, err
	}
	return &a, nil
}

// ClearOwner forgets the owner, which leaves the server unclaimed at the
// edge. Members keep their access to the server itself.
func (s *State) ClearOwner() error {
	name, err := s.ownerName()
	if err != nil || name == "" {
		return err
	}
	return s.remove(name)
}

// ownerName is the owner file of the pinned key, empty when none is
// pinned.
func (s *State) ownerName() (string, error) { return s.keyFile(ownerFile) }

// keyFile is file in the pinned key's directory, empty when no key is
// pinned.
func (s *State) keyFile(file string) (string, error) {
	key, err := s.PinnedKey()
	if err != nil || key == nil {
		return "", err
	}
	return keyDirFile(key, file), nil
}

// keyDirFile is file in key's directory.
func keyDirFile(key ed25519.PublicKey, file string) string {
	id := strings.NewReplacer("+", "-", "/", "_").Replace(strings.TrimPrefix(edgeproto.EdgeKeyFingerprint(key), "SHA256:"))
	return filepath.Join(keysDir, "sha256-"+id, file)
}

type edgeStateRecord struct {
	ServerID string `json:"server_id"`
	State    string `json:"state"`
}

// EdgeState returns the state the edge holding the pinned key last
// reported for the server serverID, edgeproto.StateClaimed or
// StateUnclaimed, empty when it never reported one or no key is pinned.
// A state recorded for another server id, which is this server before its
// host key changed, is not this server's.
func (s *State) EdgeState(serverID string) (string, error) {
	name, err := s.keyFile(edgeStateFile)
	if err != nil || name == "" {
		return "", err
	}
	var r edgeStateRecord
	if _, err = s.read(name, &r); err != nil || r.ServerID != serverID {
		return "", err
	}
	return r.State, nil
}

// writeEdgeState records state as the one the edge holding the pinned key
// last reported for the server serverID.
func (s *State) writeEdgeState(serverID, state string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	name, err := s.keyFile(edgeStateFile)
	if err != nil {
		return err
	}
	if name == "" {
		return errors.New("edgeagent: no edge key is pinned, so no edge state can be recorded")
	}
	return s.write(name, edgeStateRecord{ServerID: serverID, State: state})
}

// StoredNotices are the notices of the latest enrollment at the edge
// holding the pinned key, verified against it for the server ServerID,
// and when they came. FirstUse is set
// when that enrollment pinned the key, so they are trusted as the pin
// is: on first use, for display only.
type StoredNotices struct {
	ServerID string             `json:"server_id"`
	Received time.Time          `json:"received"`
	FirstUse bool               `json:"first_use,omitempty"`
	Notices  []edgeproto.Notice `json:"notices"`
}

// Notices returns the stored notices of the pinned key for the server
// serverID. They are none when the latest enrollment had none, no key is
// pinned, they were verified for another server id, which is this server
// before its host key changed, or they were stored under another pin
// file, as when any build pinned a key since: notices.pin, the hard link
// made when they were stored, must be the current pin file.
func (s *State) Notices(serverID string) (StoredNotices, error) {
	unlock, err := s.lock()
	if err != nil {
		return StoredNotices{}, err
	}
	defer unlock()
	cur, err := s.pinned()
	if err != nil || cur.Key == nil {
		return StoredNotices{}, err
	}
	if !sameRegular(s.path(pinFile), cur.file) || !sameRegular(s.path(keyDirFile(cur.Key, noticesPinFile)), cur.file) {
		return StoredNotices{}, nil
	}
	var n StoredNotices
	if _, err = s.read(keyDirFile(cur.Key, noticesFile), &n); err != nil || n.ServerID != serverID {
		return StoredNotices{}, err
	}
	return n, nil
}

// writeNotices replaces the stored notices of p's key, which verified
// them, with n, and forgets them when n holds none. p holds its pin file
// open. Nothing is stored unless that file still is the pin file when it
// is linked: a pin by any build since, the same key's included, stores
// nothing. Where the pin file cannot be hard linked, nothing is stored.
func (s *State) writeNotices(p pin, n StoredNotices) error {
	if p.f == nil {
		return errors.New("edgeagent: notices are stored only under a pin file held open")
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if !sameRegular(s.path(pinFile), p.file) {
		return nil
	}
	name, link := keyDirFile(p.Key, noticesFile), keyDirFile(p.Key, noticesPinFile)
	if err = s.forgetNotices(p.Key); err != nil {
		return err
	}
	// Only this build writes them, under the lock: any left is a crash's.
	dir := filepath.Dir(s.path(name))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Type().IsRegular() && noticesTemp.MatchString(e.Name()) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	if len(n.Notices) == 0 {
		return nil
	}
	payload, err := s.writeTemp(name, n)
	if err != nil {
		return err
	}
	defer os.Remove(payload) //nolint:errcheck // gone after a successful rename
	candidate := payload + ".pin"
	if s.beforeLink != nil {
		s.beforeLink()
	}
	if s.link(s.path(pinFile), candidate) != nil {
		return nil
	}
	defer os.Remove(candidate) //nolint:errcheck // gone after a successful rename
	if !sameRegular(candidate, p.file) {
		return nil
	}
	if err := os.Rename(payload, s.path(name)); err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	if err := os.Rename(candidate, s.path(link)); err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	return nil
}

// writeOwner records owner for the pinned key.
func (s *State) writeOwner(owner edgeproto.Account) error {
	name, err := s.ownerName()
	if err != nil {
		return err
	}
	if name == "" {
		return errors.New("edgeagent: no edge key is pinned, so no owner can be recorded")
	}
	return s.write(name, owner)
}

// ClaimCode returns the current claim code's record, false when there is
// none.
func (s *State) ClaimCode() (ClaimCode, bool, error) {
	var c ClaimCode
	ok, err := s.read(claimFile, &c)
	return c, ok, err
}

// IssueClaimCode replaces any claim code with a fresh one for serverID,
// valid from now for ClaimCodeTTL and ClaimCodeAttempts attempts. admin,
// when not empty, names the existing admin member the claim binds the
// claiming account to.
func (s *State) IssueClaimCode(serverID, admin string, now time.Time) (string, time.Time, error) {
	code, err := edgeproto.NewClaimCode(serverID)
	if err != nil {
		return "", time.Time{}, err
	}
	unlock, err := s.lock()
	if err != nil {
		return "", time.Time{}, err
	}
	defer unlock()
	c := ClaimCode{Hash: edgeproto.HashToken(code), ExpiresAt: now.Add(edgeproto.ClaimCodeTTL), AttemptsLeft: edgeproto.ClaimCodeAttempts,
		Admin: admin}
	if err := s.write(claimFile, c); err != nil {
		return "", time.Time{}, err
	}
	return code, c.ExpiresAt, nil
}

// RemoveClaimCode destroys the claim code.
func (s *State) RemoveClaimCode() error { return s.remove(claimFile) }

// attemptClaim spends one attempt of the claim code on presented. On a
// match it calls claim with the admin member the code names, and on
// claim's success destroys the code and records owner. A code is destroyed when it matches, and made unusable
// when it expires or runs out of attempts. The refusals are the
// edgeproto claim refusals.
func (s *State) attemptClaim(presented string, owner edgeproto.Account, now time.Time, claim func(admin string) error) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	o, err := s.Owner()
	if err != nil {
		return err
	}
	if o != nil {
		return edgeproto.RefusalClaimed
	}
	c, ok, err := s.ClaimCode()
	switch {
	case err != nil:
		return err
	case !ok:
		return edgeproto.RefusalClaimWrong
	case now.After(c.ExpiresAt):
		if c.Hash != "" {
			c.Hash = ""
			if err = s.write(claimFile, c); err != nil {
				return err
			}
		}
		return edgeproto.RefusalClaimExpired
	case c.Hash == "" || c.AttemptsLeft <= 0:
		return edgeproto.RefusalClaimExhausted
	}
	normalized, _, err := edgeproto.ParseClaimCode(presented)
	match := err == nil && subtle.ConstantTimeCompare([]byte(edgeproto.HashToken(normalized)), []byte(c.Hash)) == 1
	c.AttemptsLeft--
	if !match {
		refusal := edgeproto.RefusalClaimWrong
		if c.AttemptsLeft == 0 {
			c.Hash = ""
			refusal = edgeproto.RefusalClaimExhausted
		}
		if err := s.write(claimFile, c); err != nil {
			return err
		}
		return refusal
	}
	if err := claim(c.Admin); err != nil {
		if werr := s.write(claimFile, c); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
	// Either write alone keeps the used code from claiming again.
	return errors.Join(s.writeOwner(owner), s.remove(claimFile))
}

// transferOwner replaces the owner with owner and runs report; when
// report fails the previous owner is restored. A server without an owner
// is refused: it stays ownerless until a claim code gives it one.
func (s *State) transferOwner(origin string, owner edgeproto.Account, report func() error) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	prev, err := s.Owner()
	if err != nil {
		return err
	}
	if prev == nil {
		return fmt.Errorf("this server has no owner at %s; its administrator gives it one with a claim code from `aether-server edge claim-code`", origin)
	}
	if err := s.writeOwner(owner); err != nil {
		return err
	}
	if err := report(); err != nil {
		return errors.Join(fmt.Errorf("report the new owner to %s: %w", origin, err), s.writeOwner(*prev))
	}
	return nil
}

// clearOwnerIf forgets the owner when it still is owner, and reports
// whether it did.
func (s *State) clearOwnerIf(owner edgeproto.Account) (bool, error) {
	unlock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	cur, err := s.Owner()
	if err != nil || cur == nil || cur.Provider != owner.Provider || cur.Subject != owner.Subject {
		return false, err
	}
	return true, s.ClearOwner()
}

// policyFile, directly under StateDir, records the access policy the
// server last started with, whichever edge it used.
const policyFile = "access_policy.json"

type policyRecord struct {
	Policy edgeproto.AccessPolicy `json:"policy"`
}

// swapPolicy records p as the policy the server runs with and returns the
// one it recorded before, empty on the first start with an edge.
func swapPolicy(dataDir string, p edgeproto.AccessPolicy) (edgeproto.AccessPolicy, error) {
	dir := StateDir(dataDir)
	path := filepath.Join(dir, policyFile)
	var prev policyRecord
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", fmt.Errorf("edgeagent: %w", err)
	default:
		if err = json.Unmarshal(data, &prev); err != nil {
			return "", fmt.Errorf("edgeagent: parse %s: %w", path, err)
		}
	}
	if prev.Policy == p {
		return prev.Policy, nil
	}
	if data, err = json.Marshal(policyRecord{Policy: p}); err != nil {
		return "", fmt.Errorf("edgeagent: encode %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("edgeagent: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("edgeagent: %w", err)
	}
	return prev.Policy, nil
}

// Status returns the agent's last status, false when it never ran.
func (s *State) Status() (Status, bool, error) {
	var st Status
	ok, err := s.read(statusFile, &st)
	return st, ok, err
}

// writeStatus records st.
func (s *State) writeStatus(st Status) {
	if err := s.write(statusFile, st); err != nil {
		slog.Warn("edge: write status", "error", err)
	}
}

func (s *State) path(name string) string { return filepath.Join(s.dir, name) }

func (s *State) read(name string, v any) (bool, error) {
	data, err := os.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("edgeagent: %w", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("edgeagent: parse %s: %w", s.path(name), err)
	}
	return true, nil
}

// write replaces name atomically, so a reader in another process sees the
// old file or the new one.
func (s *State) write(name string, v any) error {
	tmp, err := s.writeTemp(name, v)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) //nolint:errcheck // gone after a successful rename
	if err := os.Rename(tmp, s.path(name)); err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	return nil
}

// writeTemp writes v to a new file beside name and returns its path.
func (s *State) writeTemp(name string, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("edgeagent: encode %s: %w", name, err)
	}
	dir := filepath.Dir(s.path(name))
	if err = s.mkdir(dir); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(name)+".*")
	if err != nil {
		return "", fmt.Errorf("edgeagent: %w", err)
	}
	if err = s.chown(f.Name()); err == nil {
		if _, err = f.Write(data); err != nil {
			err = fmt.Errorf("edgeagent: write %s: %w", f.Name(), err)
		} else if err = f.Sync(); err != nil {
			err = fmt.Errorf("edgeagent: sync %s: %w", f.Name(), err)
		}
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("edgeagent: close %s: %w", f.Name(), cerr)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (s *State) remove(name string) error {
	if err := os.Remove(s.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("edgeagent: %w", err)
	}
	return nil
}

// mkdir creates dir, within the state directory, and every directory
// between it and the state directory 0700, and narrows existing ones:
// they hold the pin that authenticates the edge.
func (s *State) mkdir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if err := os.Chmod(d, 0o700); err != nil {
			return fmt.Errorf("edgeagent: %w", err)
		}
		if err := s.chown(d); err != nil {
			return err
		}
		if d == s.dir {
			return nil
		}
	}
}

// lock serializes claim-code and owner changes between the serve process
// and the aether-server edge commands.
func (s *State) lock() (func(), error) {
	if err := s.mkdir(s.dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.path(lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("edgeagent: %w", err)
	}
	if err := s.chown(f.Name()); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("edgeagent: lock %s: %w", f.Name(), err)
	}
	return func() { _ = f.Close() }, nil
}

// chown gives path to the owner of the data directory when this process
// runs as root: `sudo aether-server edge claim-code` and `edge trust` must
// leave files a server running as that owner can read and lock.
func (s *State) chown(path string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(filepath.Dir(s.dir))
	if err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if err := os.Lchown(path, int(st.Uid), int(st.Gid)); err != nil {
		return fmt.Errorf("edgeagent: %w", err)
	}
	return nil
}
