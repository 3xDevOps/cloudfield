package quota

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
)

// Two logins of one member are read with their own tokens and cached apart,
// and an added Console login reports that it has no usage windows.
func TestReadLoginKeepsTwoLoginsOfOneMemberApart(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"test-access-default","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	logins, err := memberhome.NewLogins(filepath.Join(t.TempDir(), "logins"))
	if err != nil {
		t.Fatal(err)
	}
	sub, console := domain.LoginID("lgn-sub0000000"), domain.LoginID("lgn-console000")
	for id, identity := range map[domain.LoginID]memberhome.LoginIdentity{sub: {Kind: memberhome.LoginSubscription}, console: {Kind: memberhome.LoginConsole}} {
		dir, err := logins.Create(member, "claude", id)
		if err != nil {
			t.Fatal(err)
		}
		if id == sub {
			if err := os.WriteFile(filepath.Join(dir, memberhome.LoginCredentials), []byte(`{"claudeAiOauth":{"accessToken":"test-access-two","expiresAt":4102444800000,"scopes":["user:inference"]}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := logins.Settle(member, "claude", id, identity); err != nil {
			t.Fatal(err)
		}
	}
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		used := map[string]string{"Bearer test-access-default": "96", "Bearer test-access-two": "10"}[req.Header.Get("Authorization")]
		return fixtureResponse(`{"five_hour":{"utilization":` + used + `,"resets_at":"2100-01-01T00:00:00Z"}}`), nil
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })

	service := New(homes, logins)
	ctx := context.Background()
	def, err := service.Read(ctx, member, false)
	if err != nil {
		t.Fatal(err)
	}
	two, err := service.ReadLogin(ctx, domain.Account{Member: member, Login: sub}, false)
	if err != nil {
		t.Fatal(err)
	}
	if def[0].Windows[0].UsedPercent != 96 || two[0].Windows[0].UsedPercent != 10 {
		t.Fatalf("default %v, added %v; want 96 and 10", def[0].Windows, two[0].Windows)
	}
	if two[1].Provider != "codex" || two[1].Status != "unsupported" {
		t.Fatalf("added login's Codex row = %+v; want unsupported", two[1])
	}
	per, err := service.ReadLogin(ctx, domain.Account{Member: member, Login: console}, false)
	if err != nil || per[0].Status != "unsupported" || !strings.Contains(per[0].Error, "billed by the API") {
		t.Fatalf("Console login = %+v, %v; want unsupported, billed by the API", per, err)
	}
	if _, err := New(homes, nil).ReadLogin(ctx, domain.Account{Member: member, Login: sub}, false); err == nil {
		t.Fatal("ReadLogin without logins configured succeeded")
	}
}
