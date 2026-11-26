package edgeagent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
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
	// Key is the pinned edge key every probe enrolled under. The caller
	// writes edge-url only while it is still the pinned key.
	Key ed25519.PublicKey
	// Retired is set when the address left refused the control dial with
	// 410 and the target signed it retired; Retiring when this run's
	// verified notices name it retiring. Either way there is no moving
	// back.
	Retired, Retiring bool
}

// Move checks that target is another relay origin of the edge this server
// is enrolled with, and enrolls there once, so the server can take target
// as its edge-url. It writes no state: the edge key stays pinned, and the
// owner and claim code are kept per edge key. Each probe replaces the
// running server's control connection at the edge, which the running
// server dials again. Every probe enrolls only under the key pinned when
// Move starts, and never pins one.
//
// It refuses unless the edge speaks edgeproto.VersionRelayOrigin or later
// and announces both origins: below that a server accepts connector
// tokens only for the origin it is enrolled at, so connectors registered
// at the current one would stop working. When the current origin refuses
// the control dial with 410 Gone it is retired: the move then needs, in
// the target's own ready, a notice signed with the pinned key for this
// probe that names the current origin retired in favour of the target.
// It decides only on what this run's probes prove, never on stored
// notices, and refuses a target that either probe's verified notices name
// as retiring or retired. cmds spell the commands its refusals give.
func (a *Agent) Move(ctx context.Context, target string, cmds Commands) (Moved, error) {
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
	pinned, err := a.state.PinnedKey()
	if err != nil {
		return Moved{}, err
	}
	if pinned == nil {
		return Moved{}, fmt.Errorf("no edge key is pinned for %s yet, so nothing shows that %s is the same edge; "+
			"start the server, let it connect once, and run edge move again. %s", a.origin, to, manualMove(cmds, from, to))
	}
	first, err := a.probe(ctx, a.origin, pinned)
	var refused *DialError
	moved := Moved{To: to, Key: pinned, Retired: errors.As(err, &refused) && refused.Status == http.StatusGone}
	switch {
	case moved.Retired:
	case errors.Is(err, errNoAnswer):
		return Moved{}, fmt.Errorf("%w. %s", err, manualMove(cmds, from, to))
	case err != nil:
		return Moved{}, err
	default:
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
	second, err := a.probe(ctx, to, pinned)
	if err != nil {
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
	notices := slices.Concat(first.notices, second.notices)
	if n, ok := noticeFor(notices, to); ok {
		if replacement, cerr := edgeclient.Canonical(n.Replacement); cerr == nil && replacement == from {
			return Moved{}, fmt.Errorf("the edge says, signed with the pinned key, that %s and names %s, where this server already is; nothing changed",
				retiringClause(n), from)
		}
		return Moved{}, fmt.Errorf("the edge says, signed with the pinned key, that %s; move there instead: %s",
			retiringClause(n), cmds.Server("edge move "+n.Replacement))
	}
	if n, ok := noticeFor(notices, from); ok && n.State == edgeproto.NoticeRetiring {
		moved.Retiring = true
	}
	return moved, nil
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
		return fmt.Errorf("the edge at %s announces the relay addresses %s; moving needs both %s and %s among them: the edge's operator adds the address first",
			origin, strings.Join(announced, ", "), from, to)
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
		return fmt.Errorf("the edge at %s announces the relay addresses %s; moving needs %s among them: the edge's operator adds the address first",
			to, strings.Join(announced, ", "), to)
	}
	n, ok := noticeFor(e.notices, from)
	if !ok || n.State != edgeproto.NoticeRetired {
		return fmt.Errorf("%s; the edge at %s sends no notice signed with the pinned key that %s is retired, and a refusal alone does not show it, so nothing changed. %s",
			refused.Error(), to, from, manualMove(cmds, from, to))
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

// manualMove is what an administrator does when edge move cannot prove
// the move.
func manualMove(cmds Commands, from, to string) string {
	return fmt.Sprintf("If %s no longer answers for this edge: confirm %s and the edge key's fingerprint with whoever runs the edge, "+
		"then run %s and %s", from, to, cmds.Server("config set edge-url "+to), cmds.Restart)
}

// probe enrolls at origin under the pinned key only, and closes the
// connection once the edge is ready. Its notices are those of this
// probe's own ready, verified.
func (a *Agent) probe(ctx context.Context, origin string, only ed25519.PublicKey) (enrollment, error) {
	c, cancel, e, err := a.enroll(ctx, origin, only)
	if err != nil {
		return enrollment{}, fmt.Errorf("edgeagent: enroll at %s: %w", origin, err)
	}
	defer cancel()
	e.pin.close()
	_ = c.Close(websocket.StatusNormalClosure, "")
	return e, nil
}

// WhilePinned runs write while key is the pinned edge key, holding the
// state lock, so no pin changes in between.
func (s *State) WhilePinned(key ed25519.PublicKey, write func() error) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := s.pinned()
	if err != nil {
		return err
	}
	if !cur.Key.Equal(key) {
		return fmt.Errorf("the pinned edge key changed from %s while this ran; nothing changed, run it again", edgeproto.EdgeKeyFingerprint(key))
	}
	return write()
}
