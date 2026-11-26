package edgeagent

import (
	"fmt"
	"strings"
	"unicode"

	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// For is the stored notice for the relay origin origin.
func (n StoredNotices) For(origin string) (edgeproto.Notice, bool) {
	return noticeFor(n.Notices, origin)
}

func noticeFor(notices []edgeproto.Notice, origin string) (edgeproto.Notice, bool) {
	want, err := edgeclient.Canonical(origin)
	if err != nil {
		return edgeproto.Notice{}, false
	}
	for _, n := range notices {
		if got, err := edgeclient.Canonical(n.Origin); err == nil && got == want {
			return n, true
		}
	}
	return edgeproto.Notice{}, false
}

// MoveCommand moves a server off an address n names, on the server's
// host.
func MoveCommand(n edgeproto.Notice) string {
	return "sudo cloudfield-server edge move " + n.Replacement
}

// NoticeLine says what n means for a server enrolled at its origin and
// how to move it.
func NoticeLine(n edgeproto.Notice) string {
	return retiringClause(n) + "; move this server with: " + MoveCommand(n)
}

func retiringClause(n edgeproto.Notice) string {
	if n.State == edgeproto.NoticeRetired {
		return n.Origin + " is retired"
	}
	if n.Date != "" {
		return fmt.Sprintf("%s is retiring on %s", n.Origin, n.Date)
	}
	return n.Origin + " is retiring"
}

// Clean removes control and format characters, which can rewrite a
// terminal line, from text an edge, or whatever answered in its place,
// sent.
func Clean(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
}
