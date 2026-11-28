package localgw

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/cli"
	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// noticeTimeout is the longest edge.status waits for the edges' notices.
const noticeTimeout = 2 * time.Second

// edgeNotice is what edge.status reports of an edge address that says it
// is retiring, or that refused as retired. It is the address's own,
// unauthenticated word.
type edgeNotice struct {
	State       string `json:"state"`
	Replacement string `json:"replacement"`
	Date        string `json:"date,omitempty"`
}

// newNoticeClient is the HTTP client edge.status asks edges for notices
// with, shared by every ask and closed with the gateway.
func newNoticeClient() *http.Client {
	hc := edgeclient.HTTPClient()
	t := hc.Transport.(*http.Transport)
	t.MaxIdleConnsPerHost, t.MaxConnsPerHost, t.IdleConnTimeout = 1, 2, time.Minute
	return hc
}

// addNotices asks every edge in edges for its notice at once, through hc,
// and sets those that answered with one within noticeTimeout. Nothing is
// kept between calls.
func addNotices(ctx context.Context, hc *http.Client, dir string, edges []signedInEdge) {
	ctx, cancel := context.WithTimeout(ctx, noticeTimeout)
	defer cancel()
	notices := make([]*edgeNotice, len(edges))
	var wg sync.WaitGroup
	for i, e := range edges {
		wg.Go(func() { notices[i] = askNotice(ctx, hc, dir, e.Edge) })
	}
	wg.Wait()
	for i := range edges {
		edges[i].Retiring = notices[i]
	}
}

// askNotice reads origin's notice: nil without one, or when the edge did
// not answer. A refusal as retired is a notice too.
func askNotice(ctx context.Context, hc *http.Client, dir, origin string) *edgeNotice {
	client, err := edgeclient.NewWith(dir, origin, hc)
	if err != nil {
		return nil
	}
	r, ok, err := client.RetiringNotice(ctx)
	var refused *edgeclient.RefusedError
	switch {
	case errors.As(err, &refused) && refused.Retired != "" && !cli.SameEdge(origin, refused.Retired):
		return &edgeNotice{State: edgeproto.NoticeRetired, Replacement: refused.Retired}
	case err != nil || !ok || cli.SameEdge(origin, r.Replacement):
		return nil
	}
	return &edgeNotice{State: edgeproto.NoticeRetiring, Replacement: r.Replacement, Date: r.Date}
}
