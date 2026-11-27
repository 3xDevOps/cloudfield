package edgeagent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/coder/websocket"

	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// Commands spell the commands an administrator runs on this server's
// host, as they type them there.
type Commands struct {
	// Server is cloudfield-server with args.
	Server func(args string) string
	// Restart applies a changed edge-url.
	Restart string
}

// Moved is what Move proved.
type Moved struct {
	// To is the target as a canonical origin.
	To string
	// Retired is set when this run shows the address left retired: it
	// refused the control dial with 410 and the target signed it retired,
	// or a probe's verified notices name it retired. Retiring is set when
	// they name it retiring. Either way there is no moving back.
	Retired, Retiring bool
}

// HeldPin is the pin file in place when it was opened, held open so that
// a later pin, even of the same key, tells apart from it.
type HeldPin struct{ p pin }

// HoldPin opens the pin in place; its Key is nil when none is.
func (s *State) HoldPin() (*HeldPin, error) {
	p, err := openPin(s.path(pinFile))
	return &HeldPin{p}, err
}

// Key is the held pin's edge key.
func (h *HeldPin) Key() ed25519.PublicKey { return h.p.Key }

// Close releases the pin file.
func (h *HeldPin) Close() { h.p.close() }

// stillPinned refuses once p is no longer the pin file in place. A pin
// written by a build without the state lock can land between this check
// and what follows it.
func (s *State) stillPinned(p pin) error {
	cur, err := os.Stat(s.path(pinFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("edgeagent: %w", err)
	}
	if p.file == nil || cur == nil || !os.SameFile(cur, p.file) {
		return fmt.Errorf("the pinned edge key changed from %s while this ran; nothing changed, run it again", edgeproto.EdgeKeyFingerprint(p.Key))
	}
	return nil
}

// WhilePinned runs write under the state lock while held is still the pin
// in place.
func (s *State) WhilePinned(held *HeldPin, write func() error) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.stillPinned(held.p); err != nil {
		return err
	}
	return write()
}

// Move checks that target is another relay origin of the edge this server
// is enrolled with, and enrolls there once, so the server can take target
// as its edge-url. It writes no state: the edge key stays pinned, and the
// owner and claim code are kept per edge key. Each probe replaces the
// running server's control connection at the edge, which the running
// server dials again. Every probe enrolls only while held, the pin the
// caller opened before, is in place, and never pins one; the caller
// writes edge-url with WhilePinned on the same held pin.
//
// It refuses unless the edge speaks edgeproto.VersionRelayOrigin or later
// and announces both origins: below that a server accepts connector
// tokens only for the origin it is enrolled at, so connectors registered
// at the current one would stop working. When the current origin refuses
// the control dial with 410 Gone it is retired: the move then needs, in
// the target's own ready, a notice signed with the pinned key for this
// probe that names the current origin retired in favour of the target.
// It decides only on what this run's probes prove, never on stored
// notices, and refuses a target that a probe's verified notices name as
// retiring or retired, as soon as that probe shows it. cmds spell the
// commands its refusals give.
func (a *Agent) Move(ctx context.Context, target string, held *HeldPin, cmds Commands) (Moved, error) {
	to, err := edgeclient.Canonical(target)
	if err != nil {
		return Moved{}, fmt.Errorf("edgeagent: %w", err)
	}
	from, err := edgeclient.Canonical(a.origin)
	if err != nil {
		return Moved{}, fmt.Errorf("edgeagent: %w", err)
	}
	if to == from {
		return Moved{}, fmt.Errorf("edge-url already is %s", to)
	}
	pinned := held.Key()
	if pinned == nil {
		return Moved{}, fmt.Errorf("no edge key is pinned for %s yet, so nothing shows that %s is the same edge; "+
			"start the server, let it connect once, and run edge move again. %s", a.origin, to, manualMove(cmds, from, to, false))
	}
	first, err := a.probe(ctx, a.origin, &held.p)
	var refused *DialError
	moved := Moved{To: to, Retired: errors.As(err, &refused) && refused.Status == http.StatusGone}
	switch {
	case moved.Retired:
	case errors.Is(err, errNoAnswer):
		return Moved{}, fmt.Errorf("%w. %s", err, manualMove(cmds, from, to, true))
	case err != nil:
		return Moved{}, err
	default:
		if err = refuseRetiringTarget(first.notices, cmds, from, to); err != nil {
			return Moved{}, err
		}
		if err = checkMove(first, a.origin, from, to); err != nil {
			return Moved{}, err
		}
	}
	info, err := FetchEdgeInfo(ctx, to)
	if err != nil {
		return Moved{}, err
	}
	if !info.Key.Equal(pinned) {
		source := fmt.Sprintf("for its edge at %s, which announced it", a.origin)
		if moved.Retired {
			source = "for its edge"
		}
		return Moved{}, fmt.Errorf("%s presents edge key %s, but this server pinned %s %s: another edge answers there. "+
			"edge move only changes the address of one edge; changing edges is in docs/edge.md#turning-it-on",
			to, edgeproto.EdgeKeyFingerprint(info.Key), edgeproto.EdgeKeyFingerprint(pinned), source)
	}
	second, err := a.probe(ctx, to, &held.p)
	if err != nil {
		return Moved{}, err
	}
	if err = refuseRetiringTarget(second.notices, cmds, from, to); err != nil {
		return Moved{}, err
	}
	if moved.Retired {
		err = checkMoveFromRetired(second, refused, cmds, from, to)
	} else {
		err = checkMove(second, to, from, to)
	}
	if err != nil {
		return Moved{}, err
	}
	for _, e := range []enrollment{first, second} {
		if n, ok := noticeFor(e.notices, from); ok {
			moved.Retired = moved.Retired || n.State == edgeproto.NoticeRetired
			moved.Retiring = moved.Retiring || n.State == edgeproto.NoticeRetiring
		}
	}
	moved.Retiring = moved.Retiring && !moved.Retired
	return moved, nil
}

// refuseRetiringTarget refuses to when a probe's verified notices name it
// retiring or retired.
func refuseRetiringTarget(notices []edgeproto.Notice, cmds Commands, from, to string) error {
	n, ok := noticeFor(notices, to)
	if !ok {
		return nil
	}
	if replacement, err := edgeclient.Canonical(n.Replacement); err == nil && replacement == from {
		return fmt.Errorf("the edge says, signed with the pinned key, that %s and names %s, where this server already is; nothing changed",
			retiringClause(n), from)
	}
	return fmt.Errorf("the edge says, signed with the pinned key, that %s; move there instead: %s",
		retiringClause(n), cmds.Server("edge move "+n.Replacement))
}

// checkMove checks that the edge answering at origin speaks
// edgeproto.VersionRelayOrigin or later and announces both from and to,
// all canonical: the edge at either address must, since a client may come
// in on either.
func checkMove(e enrollment, origin, from, to string) error {
	if e.protocol < edgeproto.VersionRelayOrigin {
		return fmt.Errorf("%s speaks edge protocol %d; a server moved to %s would accept connector tokens only for that address, "+
			"so connectors registered at %s would stop working. Moving needs edge protocol %d or later: the edge's operator upgrades it",
			origin, e.protocol, to, from, edgeproto.VersionRelayOrigin)
	}
	announced, canonical := announcedOrigins(e, origin)
	if !slices.Contains(canonical, from) || !slices.Contains(canonical, to) {
		return fmt.Errorf("the edge at %s announces %s; moving needs both %s and %s among them: the edge's operator adds the address first",
			origin, announcedText(announced), from, to)
	}
	return nil
}

// checkMoveFromRetired checks the target's probe e when from refused the
// control dial with 410: the target speaks edgeproto.VersionRelayOrigin
// or later, announces itself, and its verified notices name from retired
// in favour of to. The refusal itself proves nothing.
func checkMoveFromRetired(e enrollment, refused *DialError, cmds Commands, from, to string) error {
	if e.protocol < edgeproto.VersionRelayOrigin {
		return fmt.Errorf("%s speaks edge protocol %d; moving needs edge protocol %d or later: the edge's operator upgrades it",
			to, e.protocol, edgeproto.VersionRelayOrigin)
	}
	if announced, canonical := announcedOrigins(e, to); !slices.Contains(canonical, to) {
		return fmt.Errorf("the edge at %s announces %s; moving needs %s among them: the edge's operator adds the address first",
			to, announcedText(announced), to)
	}
	n, ok := noticeFor(e.notices, from)
	if !ok || n.State != edgeproto.NoticeRetired {
		return fmt.Errorf("%s; the edge at %s sends no notice signed with the pinned key that %s is retired, and a refusal alone does not show it, so nothing changed. %s",
			refused.Error(), to, from, manualMove(cmds, from, to, true))
	}
	if replacement, err := edgeclient.Canonical(n.Replacement); err != nil || replacement != to {
		return fmt.Errorf("the edge at %s says, signed with the pinned key, that %s is retired in favour of %s; move there instead: %s",
			to, from, n.Replacement, cmds.Server("edge move "+n.Replacement))
	}
	return nil
}

// announcedOrigins are the relay origins e's ready announces, as sent and
// canonical: origin alone for an edge that announces none, which is one
// serving a single pair. A list whose every entry was unusable is no
// such edge, and announces nothing.
func announcedOrigins(e enrollment, origin string) (announced, canonical []string) {
	announced = e.ready.RelayOrigins
	if announced == nil {
		announced = []string{origin}
	}
	for _, o := range announced {
		if c, err := edgeclient.Canonical(o); err == nil {
			canonical = append(canonical, c)
		}
	}
	return announced, canonical
}

func announcedText(announced []string) string {
	if len(announced) == 0 {
		return "no relay address this server can use"
	}
	return "the relay addresses " + strings.Join(announced, ", ")
}

// manualMove is what an administrator does when edge move cannot prove
// the move. With no key pinned, edge trust pins the key whose fingerprint
// they confirmed before the restart dials the new address.
func manualMove(cmds Commands, from, to string, pinned bool) string {
	trust := ""
	if !pinned {
		trust = ", " + cmds.Server("edge trust") + " (it shows the fingerprint to compare; type yes)"
	}
	return fmt.Sprintf("If %s no longer answers for this edge: confirm %s and the edge key's fingerprint with whoever runs the edge, "+
		"then run %s%s and %s", from, to, cmds.Server("config set edge-url "+to), trust, cmds.Restart)
}

// probe enrolls at origin under the pinned key only, and closes the
// connection once the edge is ready. Its notices are those of this
// probe's own ready, verified.
func (a *Agent) probe(ctx context.Context, origin string, only *pin) (enrollment, error) {
	c, cancel, e, err := a.enroll(ctx, origin, only)
	if err != nil {
		return enrollment{}, fmt.Errorf("edgeagent: enroll at %s: %w", origin, err)
	}
	defer cancel()
	e.pin.close()
	_ = c.Close(websocket.StatusNormalClosure, "")
	return e, nil
}
