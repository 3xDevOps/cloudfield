package fallback

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/permissions"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"github.com/3xDevOps/cloudfield/internal/store"
	"github.com/3xDevOps/cloudfield/internal/store/storetest"
)

// usageByLogin answers each login's Claude quota; a login without an entry
// has no readable usage, as a Console login has none.
type usageByLogin map[domain.Account]float64

func (u usageByLogin) Read(ctx context.Context, account domain.MemberID, refresh bool) ([]quota.Provider, error) {
	return u.ReadLogin(ctx, domain.AccountOf(account), refresh)
}

func (u usageByLogin) ReadLogin(_ context.Context, account domain.Account, _ bool) ([]quota.Provider, error) {
	used, ok := u[account]
	if !ok {
		return []quota.Provider{{Provider: "claude", Status: "unsupported"}}, nil
	}
	resets := time.Now().Add(time.Hour)
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{{ID: "five_hour", UsedPercent: used, ResetsAt: &resets}}}}, nil
}

type loginWorld struct {
	db     *store.DB
	ids    map[string]domain.MemberID
	logins map[string]domain.Account
	ws     domain.WorkspaceID
}

// newLoginWorld has Lena, and Ada and Bo who share their accounts with her.
// Each named login "<member>/<name>" is added in the order given; the
// member's default login is "<member>/default".
func newLoginWorld(t *testing.T, added ...string) *loginWorld {
	t.Helper()
	ctx := context.Background()
	db, err := storetest.Open(filepath.Join(t.TempDir(), "fallback.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w := &loginWorld{db: db, ids: map[string]domain.MemberID{}, logins: map[string]domain.Account{}}
	ws := &domain.Workspace{Name: "app"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	w.ws = ws.ID
	for _, name := range []string{"Lena", "Ada", "Bo"} {
		m := &domain.Member{DisplayName: name, TailnetLogin: strings.ToLower(name) + "@example.com", Color: "#3cb44b", Role: domain.RoleCollaborator}
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := db.GrantWorkspace(ctx, ws.ID, m.ID, ""); err != nil {
			t.Fatal(err)
		}
		w.ids[name] = m.ID
		w.logins[name+"/default"] = domain.AccountOf(m.ID)
	}
	for _, owner := range []string{"Ada", "Bo"} {
		if _, err := db.ShareAccount(ctx, w.ids[owner], w.ids["Lena"]); err != nil {
			t.Fatal(err)
		}
	}
	for i, spec := range added {
		member, name, _ := strings.Cut(spec, "/")
		id := domain.LoginID("lgn-" + strings.Repeat("0", 9) + string(rune('a'+i)))
		if _, err := db.AddAgentLogin(ctx, domain.AgentLogin{MemberID: w.ids[member], Agent: "claude", ID: id, Name: name, CreatedAt: time.Now()}, ""); err != nil {
			t.Fatal(err)
		}
		w.logins[spec] = domain.Account{Member: w.ids[member], Login: id}
	}
	return w
}

func (w *loginWorld) name(a domain.Account) string {
	for name, l := range w.logins {
		if l == a {
			return name
		}
	}
	return a.String()
}

// choose measures and selects for Lena's launch on requested.
func (w *loginWorld) choose(t *testing.T, requested string, usage map[string]float64, launchable Launchable) string {
	t.Helper()
	ctx := context.Background()
	reader := usageByLogin{}
	for name, used := range usage {
		reader[w.logins[name]] = used
	}
	launcher := w.ids["Lena"]
	measured, err := Measure(ctx, w.db, reader, launcher, w.logins[requested], "claude", domain.AccountFallback{})
	if err != nil {
		t.Fatal(err)
	}
	got, excluded := Select(ctx, w.db, measured, launcher, w.logins[requested], "claude", []domain.WorkspaceID{w.ws}, domain.AccountFallback{}, launchable)
	if excluded != nil {
		t.Fatalf("excluded: %v", excluded)
	}
	return w.name(got)
}

// A spent login moves a launch to the account's next login in its owner's
// order, then to the launcher's own logins, before a teammate's account;
// and a login with unreadable usage is taken only when no readable one is
// left.
func TestSelectTriesTheAccountsLoginsThenTheLaunchersOwn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		added     []string
		requested string
		usage     map[string]float64
		want      string
	}{
		{"the account's next login", []string{"Lena/work", "Lena/spare"}, "Lena/default",
			map[string]float64{"Lena/default": 97, "Lena/work": 99, "Lena/spare": 40, "Ada/default": 0}, "Lena/spare"},
		{"own logins before a teammate's", []string{"Lena/work"}, "Ada/default",
			map[string]float64{"Ada/default": 97, "Lena/default": 80, "Lena/work": 30, "Bo/default": 0}, "Lena/default"},
		{"another account by usage when the launcher added none", nil, "Ada/default",
			map[string]float64{"Ada/default": 97, "Lena/default": 80, "Bo/default": 10}, "Bo/default"},
		{"an account served by its first login below the threshold", []string{"Bo/work"}, "Ada/default",
			map[string]float64{"Ada/default": 97, "Lena/default": 60, "Bo/default": 99, "Bo/work": 20}, "Bo/work"},
		{"unreadable only when nothing readable is left", []string{"Lena/console", "Lena/work"}, "Lena/default",
			map[string]float64{"Lena/default": 97, "Lena/work": 98, "Ada/default": 30}, "Ada/default"},
		{"unreadable last of all", []string{"Lena/console", "Lena/work"}, "Lena/default",
			map[string]float64{"Lena/default": 97, "Lena/work": 98, "Ada/default": 99, "Bo/default": 99}, "Lena/console"},
		{"a launch below the threshold stays", []string{"Lena/work"}, "Lena/default",
			map[string]float64{"Lena/default": 50, "Lena/work": 0}, "Lena/default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newLoginWorld(t, tc.added...)
			if got := w.choose(t, tc.requested, tc.usage, nil); got != tc.want {
				t.Fatalf("chose %s; want %s", got, tc.want)
			}
		})
	}
}

// A teammate is served by the owner's order and never by a login the owner
// turned automatic or use-for-shares off for; the owner's own launch still
// skips only the logins not automatic.
func TestSelectHonoursTheOwnersSwitches(t *testing.T) {
	w := newLoginWorld(t, "Ada/private", "Ada/manual", "Ada/open")
	ctx := context.Background()
	if err := w.db.SetAgentLoginShared(ctx, w.ids["Ada"], "claude", w.logins["Ada/private"].Login, false); err != nil {
		t.Fatal(err)
	}
	if err := w.db.SetAgentLoginAutomatic(ctx, w.ids["Ada"], "claude", w.logins["Ada/manual"].Login, false); err != nil {
		t.Fatal(err)
	}
	usage := map[string]float64{"Ada/default": 97, "Ada/private": 0, "Ada/manual": 0, "Ada/open": 50, "Lena/default": 99, "Bo/default": 99}
	if got := w.choose(t, "Ada/default", usage, nil); got != "Ada/open" {
		t.Fatalf("Lena's launch on Ada's spent default chose %s; want Ada/open", got)
	}
	reader := usageByLogin{}
	for name, used := range usage {
		reader[w.logins[name]] = used
	}
	measured, err := Measure(ctx, w.db, reader, w.ids["Ada"], w.logins["Ada/default"], "claude", domain.AccountFallback{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Select(ctx, w.db, measured, w.ids["Ada"], w.logins["Ada/default"], "claude", []domain.WorkspaceID{w.ws}, domain.AccountFallback{}, nil)
	if w.name(got) != "Ada/private" {
		t.Fatalf("Ada's own launch chose %s; want Ada/private, her next automatic login", w.name(got))
	}
}

// A candidate the launch cannot run on, such as a login that cannot sign
// in, is passed over for the next one.
func TestSelectSkipsALoginLaunchableRefuses(t *testing.T) {
	w := newLoginWorld(t, "Lena/work", "Lena/spare")
	refuse := func(_ context.Context, a domain.Account) (bool, error) { return a != w.logins["Lena/work"], nil }
	got := w.choose(t, "Lena/default", map[string]float64{"Lena/default": 97, "Lena/work": 10, "Lena/spare": 20}, refuse)
	if got != "Lena/spare" {
		t.Fatalf("chose %s; want Lena/spare", got)
	}
}

// A member who holds a share of a run's account reaches the run only while
// its login is on for shares; its owner always does.
func TestResolveRunLoginNeedsTheLoginShared(t *testing.T) {
	w := newLoginWorld(t, "Ada/work")
	ctx := context.Background()
	run := &domain.Run{MemberID: w.ids["Lena"], AccountMemberID: w.ids["Ada"], AccountLogin: w.logins["Ada/work"].Login, Harness: "claude"}
	if err := ResolveRunLogin(ctx, w.db, w.ids["Lena"], run, nil); err != nil {
		t.Fatalf("Lena on Ada's shared login: %v", err)
	}
	if err := w.db.SetAgentLoginShared(ctx, w.ids["Ada"], "claude", run.AccountLogin, false); err != nil {
		t.Fatal(err)
	}
	if err := ResolveRunLogin(ctx, w.db, w.ids["Lena"], run, nil); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("Lena on Ada's unshared login: %v; want denied", err)
	}
	if err := ResolveRunLogin(ctx, w.db, w.ids["Ada"], run, nil); err != nil {
		t.Fatalf("Ada on her own login: %v", err)
	}
	if err := ResolveRunLogin(ctx, w.db, w.ids["Bo"], run, nil); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("Bo, who holds no share: %v; want denied", err)
	}
}
