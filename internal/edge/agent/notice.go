package edgeagent

import (
	"fmt"
	"math"

	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// For is the stored notice for the relay origin origin. A stored entry
// that would not pass as a notice in a ready, as after the file was
// damaged, is none.
func (n StoredNotices) For(origin string) (edgeproto.Notice, bool) {
	x, ok := noticeFor(n.Notices, origin)
	if !ok || (x.State != edgeproto.NoticeRetiring && x.State != edgeproto.NoticeRetired) ||
		!edgeproto.NoticeOrigin(x.Origin) || !edgeproto.NoticeOrigin(x.Replacement) || x.Origin == x.Replacement ||
		x.Date != "" && !edgeproto.ValidDate(x.Date) {
		return edgeproto.Notice{}, false
	}
	return x, true
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

// NoticeLine says what n means for a server enrolled at its origin, and
// that move moves it.
func NoticeLine(n edgeproto.Notice, move string) string {
	return retiringClause(n) + "; move this server with: " + move
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

// Clean replaces the characters that can rewrite a terminal line, as a
// refused dial's text already has them, in text an edge, or whatever
// answered in its place, sent.
func Clean(text string) string { return printableText(text, math.MaxInt) }
