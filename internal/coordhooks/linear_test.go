package coordhooks

import (
	"strings"
	"testing"
)

func TestRoleCardNamesLinkedLinearIssueOnlyWhenLinked(t *testing.T) {
	card := RoleCard(Guide{Role: RoleInteractive, LinearIssue: "ENG-123", LinearIssueURL: "https://linear.app/acme/issue/ENG-123"})
	for _, want := range []string{"linked to Linear issue ENG-123 (https://linear.app/acme/issue/ENG-123)", `"Fixes ENG-123"`} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q: %s", want, card)
		}
	}
	if card := RoleCard(Guide{Role: RoleInteractive}); strings.Contains(card, "Linear") {
		t.Errorf("unlinked card mentions Linear: %s", card)
	}
}
