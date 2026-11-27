package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

func loginIDs(logins []domain.AgentLogin) []domain.LoginID {
	ids := make([]domain.LoginID, len(logins))
	for i, l := range logins {
		ids[i] = l.ID
	}
	return ids
}

func addLogin(t *testing.T, db *DB, member domain.MemberID, name string) domain.AgentLogin {
	t.Helper()
	id, err := NewLoginID()
	if err != nil {
		t.Fatal(err)
	}
	l, err := db.AddAgentLogin(context.Background(), domain.AgentLogin{MemberID: member, Agent: "claude", ID: id, Name: name, CreatedAt: time.Now()}, "")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func countRows(t *testing.T, db *DB, table string) int {
	t.Helper()
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A member's first added login writes the default login's row before it;
// removing the last added one leaves the member with no row at all, as a
// member who never added one, unless they turned a switch of the default
// login off: its row then keeps that choice until the switch is on again.
func TestAgentLoginsOrderAndLeaveNoRowWhenNoneAreLeft(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ada := mustCreateMember(t, db)
	if logins, err := db.ListAgentLogins(ctx, ada.ID, "claude"); err != nil || len(logins) != 0 {
		t.Fatalf("logins before any is added = %v, %v; want none", logins, err)
	}
	work := addLogin(t, db, ada.ID, "work")
	spare := addLogin(t, db, ada.ID, "spare")
	logins, err := db.ListAgentLogins(ctx, ada.ID, "claude")
	if err != nil || !slices.Equal(loginIDs(logins), []domain.LoginID{domain.DefaultLogin, work.ID, spare.ID}) || !logins[0].Automatic || !logins[2].Automatic {
		t.Fatalf("logins = %+v, %v; want default, work, spare, all automatic", logins, err)
	}
	if _, err = db.AddAgentLogin(ctx, domain.AgentLogin{MemberID: ada.ID, Agent: "claude", ID: "lgn-other00000", Name: "work", CreatedAt: time.Now()}, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second login named work: %v; want ErrConflict", err)
	}
	if err = db.RenameAgentLogin(ctx, ada.ID, "claude", spare.ID, "work"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename onto a taken name: %v; want ErrConflict", err)
	}
	if err = db.OrderAgentLogins(ctx, ada.ID, "claude", []domain.LoginID{spare.ID, domain.DefaultLogin}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an order missing a login: %v; want ErrConflict", err)
	}
	if err = db.OrderAgentLogins(ctx, ada.ID, "claude", []domain.LoginID{spare.ID, domain.DefaultLogin, work.ID}); err != nil {
		t.Fatal(err)
	}
	if err = db.SetAgentLoginAutomatic(ctx, ada.ID, "claude", work.ID, false); err != nil {
		t.Fatal(err)
	}
	perUse, err := db.AddAgentLogin(ctx, domain.AgentLogin{MemberID: ada.ID, Agent: "claude", ID: "lgn-peruse0000", Name: "console", CreatedAt: time.Now()}, "")
	if err != nil {
		t.Fatal(err)
	}
	third, err := db.AddAgentLogin(ctx, domain.AgentLogin{MemberID: ada.ID, Agent: "claude", ID: "lgn-third00000", Name: "third", CreatedAt: time.Now()}, perUse.ID)
	if err != nil {
		t.Fatal(err)
	}
	logins, err = db.ListAgentLogins(ctx, ada.ID, "claude")
	if err != nil || !slices.Equal(loginIDs(logins), []domain.LoginID{spare.ID, domain.DefaultLogin, work.ID, third.ID, perUse.ID}) || logins[2].Automatic {
		t.Fatalf("logins after reordering = %+v, %v; want spare, default, work (not automatic), third before the per-use login", logins, err)
	}
	if err := db.DeleteAgentLogin(ctx, ada.ID, "claude", domain.DefaultLogin); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove the default login: %v; want ErrNotFound", err)
	}
	for _, l := range []domain.AgentLogin{work, spare, third, perUse} {
		if err := db.DeleteAgentLogin(ctx, ada.ID, "claude", l.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(t, db, "agent_logins"); n != 0 {
		t.Fatalf("agent_logins rows after removing every added login = %d; want 0", n)
	}
	for _, set := range []func(domain.LoginID, bool) error{
		func(id domain.LoginID, on bool) error { return db.SetAgentLoginAutomatic(ctx, ada.ID, "claude", id, on) },
		func(id domain.LoginID, on bool) error { return db.SetAgentLoginShared(ctx, ada.ID, "claude", id, on) },
	} {
		off := func(id domain.LoginID) error { return set(id, false) }
		on := func(id domain.LoginID) error { return set(id, true) }
		last := addLogin(t, db, ada.ID, "last")
		if err := off(domain.DefaultLogin); err != nil {
			t.Fatal(err)
		}
		if err := db.DeleteAgentLogin(ctx, ada.ID, "claude", last.ID); err != nil {
			t.Fatal(err)
		}
		logins, err := db.ListAgentLogins(ctx, ada.ID, "claude")
		if err != nil || len(logins) != 1 || logins[0].ID != domain.DefaultLogin || (logins[0].Automatic && logins[0].Shared) {
			t.Fatalf("logins after removing the last added login with a switch of the default off = %+v, %v; want the default's row, the switch still off", logins, err)
		}
		if err := on(domain.DefaultLogin); err != nil {
			t.Fatal(err)
		}
		if n := countRows(t, db, "agent_logins"); n != 0 {
			t.Fatalf("agent_logins rows once the lone default's switch is on again = %d; want 0", n)
		}
	}
}

// Every query is keyed by the member: another member's login id is not
// found, and its owner is told only to a caller that asks by id.
func TestAgentLoginsAreKeyedByTheMember(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ada, bo := mustCreateMember(t, db), mustCreateMember(t, db)
	work := addLogin(t, db, ada.ID, "work")
	if err := db.RenameAgentLogin(ctx, bo.ID, "claude", work.ID, "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename another member's login: %v; want ErrNotFound", err)
	}
	if err := db.DeleteAgentLogin(ctx, bo.ID, "claude", work.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove another member's login: %v; want ErrNotFound", err)
	}
	if err := db.SetAgentLoginAutomatic(ctx, bo.ID, "claude", work.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set another member's login automatic: %v; want ErrNotFound", err)
	}
	if owner, err := db.AgentLoginOwner(ctx, "claude", work.ID); err != nil || owner != ada.ID {
		t.Fatalf("owner = %s, %v; want Ada", owner, err)
	}
	if err := db.DeleteMember(ctx, ada.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "agent_logins"); n != 0 {
		t.Fatalf("agent_logins rows after removing the member = %d; want 0", n)
	}
}

// A merge gives the survivor the absorbed member's added logins after its
// own, renaming one whose name it uses and off for shares; the absorbed
// member's default login is its archived home and does not come along.
func TestMergeGivesTheSurvivorTheAddedLogins(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := &domain.Member{DisplayName: "A", TailnetLogin: "a@", Color: "#3cb44b", Role: domain.RoleAdmin}
	b := &domain.Member{DisplayName: "B", PublicKey: testKey(t, "b"), Color: "#ffe119", Role: domain.RoleAdmin}
	for _, m := range []*domain.Member{a, b} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	aWork := addLogin(t, db, a.ID, "work")
	bWork := addLogin(t, db, b.ID, "work")
	bSpare := addLogin(t, db, b.ID, "spare")
	if err := db.CreateMergeCode(ctx, a.ID, "code", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	plan, err := db.PlanMerge(ctx, "code", b.ID, "", now)
	if err != nil {
		t.Fatal(err)
	}
	pm, err := db.ProposeMerge(ctx, MergeProposal{CodeHash: "code", Proposer: b.ID, Keep: a.ID, Digest: plan.Digest, Now: now, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	merge, err := db.AcceptMerge(ctx, MergeAccept{ProposalID: pm.ProposalID, Caller: a.ID, Digest: plan.Digest, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	survivor := merge.SurvivorID
	logins, err := db.ListAgentLogins(ctx, survivor, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(loginIDs(logins), []domain.LoginID{domain.DefaultLogin, aWork.ID, bWork.ID, bSpare.ID}) {
		t.Fatalf("survivor's logins = %+v; want its own, then the absorbed member's added ones", logins)
	}
	if logins[1].Name != "work" || logins[2].Name == "work" || logins[3].Name != "spare" {
		t.Fatalf("names = %q %q %q; want the absorbed work renamed", logins[1].Name, logins[2].Name, logins[3].Name)
	}
	if err := domain.ValidateLoginName(logins[2].Name); err != nil {
		t.Fatalf("renamed login %q: %v", logins[2].Name, err)
	}
	if rest, err := db.ListAgentLogins(ctx, merge.AbsorbedID, "claude"); err != nil || len(rest) != 0 {
		t.Fatalf("absorbed member's logins = %v, %v; want none", rest, err)
	}
	if !logins[0].Shared || !logins[1].Shared || logins[2].Shared || logins[3].Shared {
		t.Fatalf("shared = %+v; want the survivor's own logins as they were and the merged ones off for shares", logins)
	}
}

// A merge after removals places the absorbed member's logins after every
// one of the survivor's, picks a free name for a clash, and the plan it
// confirms lists the logins it moves.
func TestMergeAfterGapsAndClashes(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := &domain.Member{DisplayName: "A", TailnetLogin: "a@", Color: "#3cb44b", Role: domain.RoleAdmin}
	b := &domain.Member{DisplayName: "B", PublicKey: testKey(t, "b"), Color: "#ffe119", Role: domain.RoleAdmin}
	for _, m := range []*domain.Member{a, b} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	gone := addLogin(t, db, a.ID, "gone")
	addLogin(t, db, a.ID, "work")
	addLogin(t, db, a.ID, "work-2")
	if err := db.DeleteAgentLogin(ctx, a.ID, "claude", gone.ID); err != nil {
		t.Fatal(err)
	}
	bWork := addLogin(t, db, b.ID, "work")
	if err := db.CreateMergeCode(ctx, a.ID, "code", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	plan, err := db.PlanMerge(ctx, "code", b.ID, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if side := plan.Side(b.ID); side == nil || len(side.Logins) != 2 || side.Logins[1].ID != bWork.ID {
		t.Fatalf("the plan's view of B's logins = %+v; want default and work", side)
	}
	pm, err := db.ProposeMerge(ctx, MergeProposal{CodeHash: "code", Proposer: b.ID, Keep: a.ID, Digest: plan.Digest, Now: now, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	merge, err := db.AcceptMerge(ctx, MergeAccept{ProposalID: pm.ProposalID, Caller: a.ID, Digest: plan.Digest, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	logins, err := db.ListAgentLogins(ctx, merge.SurvivorID, "claude")
	if err != nil {
		t.Fatal(err)
	}
	last := logins[len(logins)-1]
	if last.ID != bWork.ID || last.Name != "work-3" {
		t.Fatalf("logins = %+v; want B's work last, renamed work-3", logins)
	}
	if _, err := db.AddAgentLogin(ctx, domain.AgentLogin{MemberID: merge.AbsorbedID, Agent: "claude", ID: "lgn-late000000", Name: "late", CreatedAt: now}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a login added for the absorbed member: %v; want ErrNotFound", err)
	}
}

// Two members never hold one login id.
func TestALoginIDBelongsToOneMember(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ada, bo := mustCreateMember(t, db), mustCreateMember(t, db)
	if _, err := db.AddAgentLogin(ctx, domain.AgentLogin{MemberID: ada.ID, Agent: "claude", ID: "lgn-same000000", Name: "work", CreatedAt: time.Now()}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddAgentLogin(ctx, domain.AgentLogin{MemberID: bo.ID, Agent: "claude", ID: "lgn-same000000", Name: "work", CreatedAt: time.Now()}, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("Bo taking Ada's login id: %v; want ErrConflict", err)
	}
}

// A login added before one that was removed meanwhile goes last.
func TestAddBeforeARemovedLoginGoesLast(t *testing.T) {
	db := openTestDB(t)
	ada := mustCreateMember(t, db)
	work := addLogin(t, db, ada.ID, "work")
	l, err := db.AddAgentLogin(context.Background(), domain.AgentLogin{MemberID: ada.ID, Agent: "claude", ID: "lgn-late000000", Name: "late", CreatedAt: time.Now()}, "lgn-gone000000")
	if err != nil || l.Position <= work.Position {
		t.Fatalf("added = %+v, %v; want it after work", l, err)
	}
}
