package edgeagent

import (
	"crypto/ed25519"
	"slices"
	"testing"

	"golang.org/x/crypto/ssh"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

func TestConnectorOrigins(t *testing.T) {
	origins := []string{"https://relay-old.example.test", "https://relay-new.example.test"}
	for _, tt := range []struct {
		name  string
		live  *session
		state string
		want  []string
	}{
		{"claimed at relay-origin protocol", &session{protocol: edgeproto.VersionRelayOrigin, origins: origins, announced: origins}, edgeproto.StateClaimed, origins},
		{"announced without the dialed origin", &session{protocol: edgeproto.VersionRelayOrigin, origins: origins, announced: origins[1:]}, edgeproto.StateClaimed, origins[1:]},
		{"none announced", &session{protocol: edgeproto.VersionRelayOrigin, origins: origins[:1]}, edgeproto.StateClaimed, origins[:1]},
		{"no control connection", nil, edgeproto.StateClaimed, nil},
		{"before relay origins", &session{protocol: edgeproto.VersionRelayOrigin - 1, origins: origins}, edgeproto.StateClaimed, nil},
		{"unclaimed", &session{protocol: edgeproto.VersionRelayOrigin, origins: origins}, edgeproto.StateUnclaimed, nil},
		{"no state yet", &session{protocol: edgeproto.VersionRelayOrigin, origins: origins}, "", nil},
		{"listed only unusable origins", &session{protocol: edgeproto.VersionRelayOrigin, origins: origins[:1], announced: decodedOrigins(t, []string{"ftp://relay.example.test"})}, edgeproto.StateClaimed, nil},
		{"sent no list", &session{protocol: edgeproto.VersionRelayOrigin, origins: origins[:1], announced: decodedOrigins(t, nil)}, edgeproto.StateClaimed, origins[:1]},
	} {
		a := &Agent{origin: origins[0], live: tt.live, edgeState: tt.state}
		if got := a.ConnectorOrigins(); !slices.Equal(got, tt.want) {
			t.Errorf("%s: ConnectorOrigins() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// decodedOrigins are the relay origins of a ready that lists list, nil
// for none sent, as the server decodes them.
func decodedOrigins(t *testing.T, list []string) []string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	data, err := edgeproto.EncodeControl(edgeproto.Ready{ServerID: edgeproto.ServerID(host), State: edgeproto.StateClaimed, EdgeKey: pub, RelayOrigins: list})
	if err != nil {
		t.Fatal(err)
	}
	m, err := edgeproto.DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}
	return m.(edgeproto.Ready).RelayOrigins
}
