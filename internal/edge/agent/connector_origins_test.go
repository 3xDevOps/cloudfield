package edgeagent

import (
	"slices"
	"testing"

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
	} {
		a := &Agent{origin: origins[0], live: tt.live, edgeState: tt.state}
		if got := a.ConnectorOrigins(); !slices.Equal(got, tt.want) {
			t.Errorf("%s: ConnectorOrigins() = %v, want %v", tt.name, got, tt.want)
		}
	}
}
