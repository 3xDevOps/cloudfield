package localgw

import (
	"context"
	"errors"
	"sync"
	"time"

	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

const (
	// noticeRecheck is how long an edge's answer to GET /v1/edge is
	// reused before edge.status asks again.
	noticeRecheck = 5 * time.Minute
	// noticeMaxAge bounds how long a notice is shown after the edge last
	// answered at all.
	noticeMaxAge = time.Hour
	// noticeTimeout is the longest edge.status waits for one edge.
	noticeTimeout = 2 * time.Second
)

// edgeNotice is what edge.status reports of an edge address that says it
// is retiring, or that refused as retired. It is the address's own,
// unauthenticated word. Known is whether this machine already has its
// own reason to know Replacement, as cli.EdgeHoldings.Known says.
type edgeNotice struct {
	State       string `json:"state"`
	Replacement string `json:"replacement"`
	Date        string `json:"date,omitempty"`
	Known       bool   `json:"known"`
}

// edgeNotices caches each signed-in edge's notice by relay origin.
type edgeNotices struct {
	mu  sync.Mutex
	by  map[string]cachedNotice
	now func() time.Time
}

type cachedNotice struct {
	notice *edgeNotice
	// checked is when the edge was last asked, answered when it last
	// answered.
	checked, answered time.Time
}

func newEdgeNotices() *edgeNotices {
	return &edgeNotices{by: map[string]cachedNotice{}, now: time.Now}
}

// lookup returns the notice of each of origins that has one, asking in
// parallel every edge not asked within noticeRecheck.
func (n *edgeNotices) lookup(ctx context.Context, dir string, origins []string) map[string]edgeNotice {
	var wg sync.WaitGroup
	n.mu.Lock()
	now := n.now()
	for _, origin := range origins {
		entry := n.by[origin]
		if now.Sub(entry.checked) < noticeRecheck {
			continue
		}
		entry.checked = now
		n.by[origin] = entry
		wg.Go(func() {
			notice, err := askNotice(ctx, dir, origin)
			n.mu.Lock()
			defer n.mu.Unlock()
			entry, ok := n.by[origin]
			if !ok {
				return
			}
			if err == nil {
				entry.notice, entry.answered = notice, n.now()
			}
			n.by[origin] = entry
		})
	}
	n.mu.Unlock()
	wg.Wait()
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[string]edgeNotice{}
	now = n.now()
	for _, origin := range origins {
		if entry := n.by[origin]; entry.notice != nil && now.Sub(entry.answered) <= noticeMaxAge {
			out[origin] = *entry.notice
		}
	}
	return out
}

// askNotice reads origin's notice, nil when it carries none. A refusal
// as retired is a notice too; any other failure is an error, which keeps
// what the cache holds.
func askNotice(ctx context.Context, dir, origin string) (*edgeNotice, error) {
	client, err := edgeclient.New(dir, origin)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, noticeTimeout)
	defer cancel()
	r, ok, err := client.RetiringNotice(ctx)
	var refused *edgeclient.RefusedError
	switch {
	case errors.As(err, &refused) && refused.Retired != "":
		return &edgeNotice{State: edgeproto.NoticeRetired, Replacement: refused.Retired}, nil
	case err != nil:
		return nil, err
	case !ok:
		return nil, nil
	}
	return &edgeNotice{State: edgeproto.NoticeRetiring, Replacement: r.Replacement, Date: r.Date}, nil
}

// forget drops what the cache holds for origin.
func (n *edgeNotices) forget(origin string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.by, origin)
}

func (n *edgeNotices) reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	clear(n.by)
}
