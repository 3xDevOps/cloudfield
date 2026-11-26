package edgeagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/coder/websocket"

	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// Move checks that target is another relay origin of the edge this server
// is enrolled with, and enrolls there once, so the server can take target
// as its edge-url. It returns target as a canonical origin, and whether
// the address left is retired. It writes no state: the edge key stays
// pinned, and the owner and claim code are kept per edge key. Each probe
// replaces the running server's control connection at the edge, which
// the running server dials again.
//
// It refuses unless the edge speaks edgeproto.VersionRelayOrigin or later
// and announces both origins: below that a server accepts connector
// tokens only for the origin it is enrolled at, so connectors registered
// at the current one would stop working. When the current origin refuses
// the control dial with 410 Gone it is retired: the move then needs, in
// the target's own ready, a notice signed with the pinned key for this
// probe that names the current origin retired in favour of the target.
// It decides only on what this run's probes prove, never on stored
// notices, and refuses a target that its own ready names as retiring.
func (a *Agent) Move(ctx context.Context, target string) (string, bool, error) {
	to, err := edgeclient.Canonical(target)
	if err != nil {
		return "", false, fmt.Errorf("edgeagent: %w", err)
	}
	from, err := edgeclient.Canonical(a.origin)
	if err != nil {
		return "", false, fmt.Errorf("edgeagent: %w", err)
	}
	if to == from {
		return "", false, fmt.Errorf("edge-url already is %s", to)
	}
	pinned, err := a.state.PinnedKey()
	if err != nil {
		return "", false, err
	}
	if pinned == nil {
		return "", false, fmt.Errorf("no edge key is pinned for %s yet, so nothing shows that %s is the same edge; "+
			"start the server, let it connect once, and run edge move again. %s", a.origin, to, manualMove(from, to))
	}
	e, err := a.probe(ctx, a.origin)
	var refused *DialError
	retired := errors.As(err, &refused) && refused.Status == http.StatusGone
	var noAnswer *url.Error
	switch {
	case retired:
	case refused == nil && errors.As(err, &noAnswer):
		return "", false, fmt.Errorf("%w. %s", err, manualMove(from, to))
	case err != nil:
		return "", false, err
	default:
		if err = checkMove(e, a.origin, from, to); err != nil {
			return "", false, err
		}
	}
	info, err := FetchEdgeInfo(ctx, to)
	if err != nil {
		return "", false, err
	}
	if !bytes.Equal(info.Key, pinned) {
		return "", false, fmt.Errorf("%s presents edge key %s, but this server pinned %s for its edge at %s, which announced it: another edge answers there. "+
			"edge move only changes the address of one edge; changing edges is in docs/edge.md#turning-it-on",
			to, edgeproto.EdgeKeyFingerprint(info.Key), edgeproto.EdgeKeyFingerprint(pinned), a.origin)
	}
	if e, err = a.probe(ctx, to); err != nil {
		return "", false, err
	}
	if retired {
		err = checkMoveFromRetired(e, refused, from, to)
	} else {
		err = checkMove(e, to, from, to)
	}
	if err != nil {
		return "", false, err
	}
	if n, ok := noticeFor(e.notices, to); ok && n.State == edgeproto.NoticeRetiring {
		return "", false, fmt.Errorf("the edge at %s says, signed with the pinned key, that %s; move there instead: edge move %s",
			to, retiringClause(n), n.Replacement)
	}
	return to, retired, nil
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
func checkMoveFromRetired(e enrollment, refused *DialError, from, to string) error {
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
		return fmt.Errorf("%s; the edge at %s sends no notice signed with the pinned key that %s is retired, and a refusal alone does not show it, so nothing changed",
			Clean(refused.Error()), to, from)
	}
	if replacement, err := edgeclient.Canonical(n.Replacement); err != nil || replacement != to {
		return fmt.Errorf("the edge at %s says, signed with the pinned key, that %s is retired in favour of %s; move there instead: edge move %s",
			to, from, n.Replacement, n.Replacement)
	}
	return nil
}

// announcedOrigins are the relay origins e's ready announces, origin
// alone for an edge that announces none, as sent and canonical.
func announcedOrigins(e enrollment, origin string) (announced, canonical []string) {
	announced = e.ready.RelayOrigins
	if len(announced) == 0 {
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
func manualMove(from, to string) string {
	return fmt.Sprintf("If %s no longer answers for this edge: confirm %s and the edge key's fingerprint with whoever runs the edge, "+
		"then run sudo cloudfield-server config set edge-url %s and sudo systemctl restart aether-server", from, to, to)
}

// probe enrolls at origin and closes the connection once the edge is
// ready. Its notices are those of this probe's own ready, verified.
func (a *Agent) probe(ctx context.Context, origin string) (enrollment, error) {
	c, cancel, e, err := a.enroll(ctx, origin)
	if err != nil {
		return enrollment{}, fmt.Errorf("edgeagent: enroll at %s: %w", origin, err)
	}
	defer cancel()
	e.pin.close()
	_ = c.Close(websocket.StatusNormalClosure, "")
	return e, nil
}
