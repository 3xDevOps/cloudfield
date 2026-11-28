package sshd

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/store"
)

func init() {
	registerMethod(protocol.MethodConnectorCode, (*Server).connectorCode)
	registerMethod(protocol.MethodConnectorList, (*Server).connectorList)
	registerMethod(protocol.MethodConnectorRevoke, (*Server).connectorRevoke)
}

// ConnectorCodeTTL is how long a connector pairing code lives.
const ConnectorCodeTTL = 10 * time.Minute

// ErrConnectorCode means a pairing code is no live code: wrong, expired
// or spent.
var ErrConnectorCode = errors.New("sshd: no such connector code")

// ConnectorGrant is whom a connector is authorized for: the member who
// minted its pairing code and the edge device they minted it on, empty
// over a member SSH key or a tailnet connection; or, for a connector
// authorized by signing in at the edge instead of a code, the member and
// the edge account (Provider, Subject) that signed in.
type ConnectorGrant struct {
	Member   domain.MemberID
	Device   domain.DeviceID
	Provider string
	Subject  string
}

// connectorCode is a live pairing code, kept as the SHA-256 of its
// normalized form and never stored.
type connectorCode struct {
	grant   ConnectorGrant
	expires time.Time
}

// ConnectorLabel names a connector in audit text and errors: ChatGPT
// connector con-xxx.
func ConnectorLabel(client, id string) string {
	return client + " connector " + id
}

// ConnectorURL is the URL members add as a custom connector,
// <edge origin>/mcp/<server id>, or empty while the server has no edge.
func (s *Server) ConnectorURL() string {
	edge := s.currentEdgeOwner()
	if edge == nil {
		return ""
	}
	return edge.Origin() + edgeproto.MCPPath(edgeproto.ServerID(s.hostKey.PublicKey()))
}

// UniversalConnectorURL is the one connector URL of every server on the
// edge, <edge origin>/mcp, while the edge speaks
// edgeproto.VersionUniversalMCP, and empty otherwise.
func (s *Server) UniversalConnectorURL() string {
	edge := s.currentEdgeOwner()
	if edge == nil || edge.EdgeVersion() < edgeproto.VersionUniversalMCP {
		return ""
	}
	return edge.Origin() + edgeproto.PathUniversalMCP
}

func (s *Server) EdgeAccess() edgeproto.AccessPolicy { return s.cfg.EdgeAccess }

// EdgeBindsSignIn reports whether the edge speaks
// edgeproto.VersionConnectorSignIn: it posts a ConnectorAssertion bound to
// the browser and the redirect URI. An older edge puts it in the URL.
func (s *Server) EdgeBindsSignIn() bool {
	edge := s.currentEdgeOwner()
	return edge != nil && edge.EdgeVersion() >= edgeproto.VersionConnectorSignIn
}

// VerifyConnectorAssertion checks an edge's assertion that an account
// chose this server for the authorization request of clientID with
// challenge and redirectURI, against the key of the edge the server is
// connected to.
func (s *Server) VerifyConnectorAssertion(signed, clientID, challenge, redirectURI string, now time.Time) (edgeproto.ConnectorAssertion, error) {
	edge := s.currentEdgeOwner()
	if edge == nil {
		return edgeproto.ConnectorAssertion{}, errors.New("this server has no edge")
	}
	key := edge.EdgeKey()
	if key == nil {
		return edgeproto.ConnectorAssertion{}, fmt.Errorf("this server is not connected to its edge at %s", edge.Origin())
	}
	return edgeproto.VerifyConnectorAssertion(key, signed, edge.Origin(), edgeproto.ServerID(s.hostKey.PublicKey()), clientID, challenge, redirectURI, now)
}

// ConnectorAccountGrant is the grant of a connector authorized by
// signing in at the edge as account, and the name of its member. Only
// edge-access account admits one, as it admits a device by signing in
// alone, and only for an account bound to a member.
func (s *Server) ConnectorAccountGrant(ctx context.Context, account edgeproto.Account) (ConnectorGrant, string, error) {
	if s.cfg.EdgeAccess != edgeproto.PolicyAccount {
		return ConnectorGrant{}, "", fmt.Errorf("%s admits approved devices only (edge-access %s), so a connector needs a code from aether mcp",
			cmp.Or(s.cfg.Name, "this server"), s.cfg.EdgeAccess)
	}
	ids, err := s.identityStore()
	if err != nil {
		return ConnectorGrant{}, "", err
	}
	m, err := ids.GetMemberByIdentity(ctx, account.Provider, account.Subject)
	switch {
	case errors.Is(err, store.ErrNotFound) && account.Provider == edgeproto.ProviderReviewer:
		return ConnectorGrant{}, "", fmt.Errorf("%s is bound to no member of %s; an admin binds it with aether member link --reviewer %s <member-id>",
			accountName(account), cmp.Or(s.cfg.Name, "this server"), account.Subject)
	case errors.Is(err, store.ErrNotFound):
		return ConnectorGrant{}, "", fmt.Errorf("%s is not a member of %s; an admin invites it, or a member connects with a code from aether mcp",
			accountName(account), cmp.Or(s.cfg.Name, "this server"))
	}
	if err != nil {
		return ConnectorGrant{}, "", fmt.Errorf("sshd: connector account: %w", err)
	}
	if err = s.checkReviewer(account, m); err != nil {
		return ConnectorGrant{}, "", err
	}
	return ConnectorGrant{Member: m.ID, Provider: account.Provider, Subject: account.Subject}, cmp.Or(m.DisplayName, string(m.ID)), nil
}

// Name is the server's host name.
func (s *Server) Name() string { return s.cfg.Name }

func (s *Server) connectorStore() (store.ConnectorStore, *protocol.Error) {
	cs, ok := s.cfg.Store.(store.ConnectorStore)
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the store does not keep connectors"}
	}
	return cs, nil
}

func connectorCodeHash(code string) string {
	sum := sha256.Sum256([]byte(store.NormalizeApprovalCode(code)))
	return hex.EncodeToString(sum[:])
}

// edgeNotices are the verified notices of the server's edge by the relay
// origin each is about, and the relay origins the edge takes connectors
// at now (EdgeOwner.ConnectorOrigins).
type edgeNotices struct {
	byOrigin map[string]edgeproto.Notice
	firstUse bool
	// relays maps each announced origin's canonical form to its spelling
	// as announced, which the relay forwards and publishes.
	relays map[string]string
}

// edgeNotices reads the edge's notices. One that cannot be read is
// logged, and connectors are then shown as if there were none.
func (s *Server) edgeNotices() edgeNotices {
	edge := s.currentEdgeOwner()
	if edge == nil {
		return edgeNotices{}
	}
	stored, err := edge.Notices()
	if err != nil {
		msg := err.Error()
		if last := s.connectorNoticesErr.Swap(&msg); last == nil || *last != msg {
			slog.Warn("sshd: connectors shown without the edge's notices", "error", err)
		}
		return edgeNotices{}
	}
	s.connectorNoticesErr.Store(nil)
	if len(stored.Notices) == 0 {
		return edgeNotices{}
	}
	out := edgeNotices{byOrigin: make(map[string]edgeproto.Notice, len(stored.Notices)), firstUse: stored.FirstUse, relays: map[string]string{}}
	for _, n := range stored.Notices {
		if o, err := edgeclient.Canonical(n.Origin); err == nil {
			out.byOrigin[o] = n
		}
	}
	for _, relay := range edge.ConnectorOrigins() {
		if o, err := edgeclient.Canonical(relay); err == nil {
			out.relays[o] = relay
		}
	}
	return out
}

// noticeFor is what notices say about the connector URL u of this
// server, nil when they say nothing. Origins compare in their canonical
// form, as a resource keeps the spelling of the origin it was added at.
// Its URL, u's path at the replacement as the edge announced it, is set
// only while the edge takes connectors there and the notices did not
// come with the pin, which nothing vouches for.
func (s *Server) noticeFor(notices edgeNotices, u string) *protocol.ConnectorNotice {
	if len(notices.byOrigin) == 0 {
		return nil
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return nil
	}
	origin, err := edgeclient.Canonical(parsed.Scheme + "://" + parsed.Host)
	n, ok := notices.byOrigin[origin]
	path := parsed.Path
	if err != nil || !ok || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(path != edgeproto.PathUniversalMCP && path != edgeproto.MCPPath(edgeproto.ServerID(s.hostKey.PublicKey()))) {
		return nil
	}
	out := &protocol.ConnectorNotice{State: n.State, Date: n.Date}
	if replacement, err := edgeclient.Canonical(n.Replacement); err == nil && !notices.firstUse && notices.relays[replacement] != "" {
		out.URL = notices.relays[replacement] + path
	}
	return out
}

// ConnectorNotice is what the edge's verified notices say about the
// connector URL u of this server, nil when they say nothing.
func (s *Server) ConnectorNotice(u string) *protocol.ConnectorNotice {
	return s.noticeFor(s.edgeNotices(), u)
}

// offered is the connector URL u at the address notices name instead of
// u's when noticeFor names it, and u otherwise.
func (s *Server) offered(notices edgeNotices, u string) string {
	if n := s.noticeFor(notices, u); n != nil && n.URL != "" {
		return n.URL
	}
	return u
}

// OnUniversal reports whether member adds connectors at the universal
// URL: the edge speaks it and member has a GitHub account bound.
func (s *Server) OnUniversal(ctx context.Context, member domain.MemberID) (bool, error) {
	if s.UniversalConnectorURL() == "" {
		return false, nil
	}
	return s.githubLinked(ctx, member)
}

// githubLinked reports whether member has a GitHub account bound, which
// the edge needs to offer the server at the universal URL.
func (s *Server) githubLinked(ctx context.Context, member domain.MemberID) (bool, error) {
	ids, err := s.identityStore()
	if err != nil {
		return false, err
	}
	all, err := ids.ListIdentities(ctx)
	if err != nil {
		return false, fmt.Errorf("sshd: connector url: %w", err)
	}
	return slices.ContainsFunc(all, func(id *domain.Identity) bool {
		return id.Member == member && id.Provider == edgeproto.ProviderGitHub
	}), nil
}

// connectorURLFor is the URL member adds as a connector, and whether it is
// the universal one: universal, UniversalConnectorURL as the caller read
// it, while not empty and member has a GitHub account bound, the server's
// own URL otherwise; either at the address notices name instead of a
// retiring or retired one.
func (s *Server) connectorURLFor(ctx context.Context, member domain.MemberID, universal string, notices edgeNotices) (string, bool, error) {
	if universal == "" {
		return s.offered(notices, s.ConnectorURL()), false, nil
	}
	linked, err := s.githubLinked(ctx, member)
	if err != nil {
		return "", false, err
	}
	if linked {
		return s.offered(notices, universal), true, nil
	}
	return s.offered(notices, s.ConnectorURL()), false, nil
}

// refuseEdgeSignIn refuses method to a connector authorized by signing
// in at the edge: a code or link it minted would outlive the checks that
// cut the connector off.
func refuseEdgeSignIn(ctx context.Context, method string) *protocol.Error {
	id, _ := ctx.Value(connIdentityKey{}).(connIdentity)
	if !id.edgeSignIn {
		return nil
	}
	return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
		"%s needs a device, a member SSH key or a tailnet connection; %s was authorized by signing in at the edge, with no device",
		method, ConnectorLabel(id.connectorClient, id.connector))}
}

// connectorCode mints the caller a pairing code, replacing any code of
// theirs not yet spent, so a member holds at most one.
func (s *Server) connectorCode(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodConnectorCode
	if perr := refuseEdgeSignIn(ctx, method); perr != nil {
		return nil, perr
	}
	if perr := s.requireApprovedCaller(ctx, method, false); perr != nil {
		return nil, perr
	}
	if _, perr := s.connectorStore(); perr != nil {
		return nil, perr
	}
	edge := s.currentEdgeOwner()
	if edge == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: fmt.Sprintf(
			"%s: this server has no edge (edge-url is empty), and ChatGPT and Claude reach a connector only through one; "+
				"an admin turns it on with: sudo aether-server config set edge-url %s, then sudo systemctl restart aether-server",
			method, edgeclient.DefaultURL)}
	}
	switch version := edge.EdgeVersion(); {
	case version == 0:
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: fmt.Sprintf(
			"%s: this server is not connected to its edge at %s; an admin sees why with: sudo aether-server edge status",
			method, edge.Origin())}
	case version < edgeproto.VersionHTTP:
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: fmt.Sprintf(
			"%s: edge %s speaks edge protocol %d and cannot forward connectors; its operator upgrades it",
			method, edge.Origin(), version)}
	}
	connectorURL, _, err := s.connectorURLFor(ctx, member, s.UniversalConnectorURL(), s.edgeNotices())
	if err != nil {
		return nil, rpcError(err)
	}
	code, err := store.RandomApprovalCode()
	if err != nil {
		return nil, rpcError(err)
	}
	id, _ := ctx.Value(connIdentityKey{}).(connIdentity)
	now := time.Now()
	expires := now.Add(ConnectorCodeTTL)
	s.connectorMu.Lock()
	if s.connectorCodes == nil {
		s.connectorCodes = make(map[string]connectorCode)
	}
	maps.DeleteFunc(s.connectorCodes, func(_ string, c connectorCode) bool {
		return c.grant.Member == member || !now.Before(c.expires)
	})
	s.connectorCodes[connectorCodeHash(code)] = connectorCode{grant: ConnectorGrant{Member: member, Device: id.device}, expires: expires}
	s.connectorMu.Unlock()
	slog.Info("sshd: connector code minted", "member", member, "device", id.device)
	return protocol.ConnectorCodeResult{URL: connectorURL, Code: code,
		ExpiresAt: expires.UTC().Format(time.RFC3339)}, nil
}

// RedeemConnectorCode spends code, as a person typed it: any case, the
// dash optional. It is ErrConnectorCode unless code is live at now.
func (s *Server) RedeemConnectorCode(code string, now time.Time) (ConnectorGrant, error) {
	hash := connectorCodeHash(code)
	s.connectorMu.Lock()
	defer s.connectorMu.Unlock()
	c, ok := s.connectorCodes[hash]
	if !ok {
		return ConnectorGrant{}, ErrConnectorCode
	}
	delete(s.connectorCodes, hash)
	if !now.Before(c.expires) {
		return ConnectorGrant{}, ErrConnectorCode
	}
	return c.grant, nil
}

// CheckConnectorGrant re-checks a grant as each connector request does:
// the member still exists and, when the code was minted on an edge
// device, that device still admits a connection under the current
// edge-access policy. A revoked device never does; under approved-devices
// only an approved one does. A grant of an edge account holds only under
// edge-access account and while the account is bound to the member.
func (s *Server) CheckConnectorGrant(ctx context.Context, g ConnectorGrant) error {
	if g.Subject == "" {
		return s.checkConnIdentity(ctx, connIdentity{member: g.Member, device: g.Device})
	}
	account := edgeproto.Account{Provider: g.Provider, Subject: g.Subject}
	if s.cfg.EdgeAccess != edgeproto.PolicyAccount {
		return fmt.Errorf("it was authorized by signing in at the edge as %s, which edge-access %s does not admit; connect again with a code from aether mcp",
			accountName(account), s.cfg.EdgeAccess)
	}
	ids, err := s.identityStore()
	if err != nil {
		return err
	}
	m, err := ids.GetMemberByIdentity(ctx, g.Provider, g.Subject)
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && m.ID != g.Member):
		return fmt.Errorf("%s, which authorized it, is no longer bound to member %s", accountName(account), g.Member)
	case err != nil:
		return fmt.Errorf("sshd: connector account: %w", err)
	}
	return s.checkReviewer(account, m)
}

// checkReviewer refuses the edge's reviewer account unless edge-reviewer
// is on and its member is no admin: a reviewer's password is shared
// with an app directory.
func (s *Server) checkReviewer(account edgeproto.Account, m *domain.Member) error {
	switch {
	case account.Provider != edgeproto.ProviderReviewer:
		return nil
	case !s.cfg.EdgeReviewer:
		return fmt.Errorf("%s does not take the edge's reviewer account: edge-reviewer is off. An admin turns it on on the server's host with "+
			"sudo aether-server config set edge-reviewer true, then sudo systemctl restart aether-server", cmp.Or(s.cfg.Name, "this server"))
	case m.Role.Admin():
		return fmt.Errorf("%s is bound to member %s, an admin, and a reviewer may act only as a viewer or collaborator; "+
			"a super admin changes the member's role with aether member role", accountName(account), m.ID)
	}
	return nil
}

func connectorToWire(c *store.Connector) protocol.Connector {
	out := protocol.Connector{ID: c.ID, MemberID: string(c.Member), ClientID: c.ClientID, ClientName: c.ClientName, RedirectHost: c.RedirectHost,
		DeviceID: string(c.Device), Resource: c.Resource, Scopes: c.Scopes, CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339)}
	if c.LastUsedAt != nil {
		out.LastUsedAt = c.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (s *Server) connectorList(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	cs, perr := s.connectorStore()
	if perr != nil {
		return nil, perr
	}
	list, err := cs.ListConnectors(ctx, member, time.Now())
	if err != nil {
		return nil, rpcError(err)
	}
	notices := s.edgeNotices()
	universal := s.UniversalConnectorURL()
	connectorURL, onUniversal, err := s.connectorURLFor(ctx, member, universal, notices)
	if err != nil {
		return nil, rpcError(err)
	}
	// The universal URL moves only for a member the edge offers it to.
	if onUniversal {
		universal = s.offered(notices, universal)
	}
	out := protocol.ConnectorListResult{Connectors: make([]protocol.Connector, 0, len(list)), URL: connectorURL, UniversalURL: universal,
		SignIn: universal != "" && connectorURL == universal && s.cfg.EdgeAccess == edgeproto.PolicyAccount}
	if edge := s.currentEdgeOwner(); universal != "" && edge != nil {
		for _, o := range edge.RelayOrigins() {
			out.UniversalURLs = append(out.UniversalURLs, o+edgeproto.PathUniversalMCP)
		}
	}
	for _, c := range list {
		wire := connectorToWire(c)
		wire.Notice = s.noticeFor(notices, c.Resource)
		// The edge offers the universal URL only to an account it lists
		// for this server.
		if wire.Notice != nil && !onUniversal && strings.HasSuffix(wire.Notice.URL, edgeproto.PathUniversalMCP) {
			wire.Notice.URL = ""
		}
		out.Connectors = append(out.Connectors, wire)
	}
	return out, nil
}

func (s *Server) connectorRevoke(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodConnectorRevoke
	p, perr := decodeParams[protocol.ConnectorRevokeParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.ConnectorID == "" {
		return nil, invalidParams("connector_id is required")
	}
	cs, perr := s.connectorStore()
	if perr != nil {
		return nil, perr
	}
	unknown := &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf(
		"%s: no connector %s; aether mcp lists yours", method, p.ConnectorID)}
	c, err := cs.GetConnector(ctx, p.ConnectorID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, unknown
	}
	if err != nil {
		return nil, rpcError(err)
	}
	if c.Member != member && !s.superAdmin(ctx, member) {
		return nil, unknown
	}
	if err := cs.DeleteConnector(ctx, c.ID); err != nil {
		return nil, rpcError(err)
	}
	noteChange(ctx)
	slog.Info("sshd: connector revoked", "actor", member, "connector", c.ID, "member", c.Member)
	return protocol.ConnectorRevokeResult{Connector: connectorToWire(c)}, nil
}
