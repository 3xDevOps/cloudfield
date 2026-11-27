package localgw

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/cli"
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

// edgeNotices caches each signed-in edge's notice by relay origin. gen
// counts sign-ins, logouts and resets: a lookup on signed-in rows read
// before one of them neither adds nor prunes entries.
type edgeNotices struct {
	mu   sync.Mutex
	by   map[string]*cachedNotice
	gen  uint64
	now  func() time.Time
}

// cachedNotice is one edge's entry. A result is kept only while the entry
// is still the one in the map, so a late answer from before a logout or a
// new sign-in is dropped.
type cachedNotice struct {
	notice *edgeNotice
	// checked is when the edge was last asked, answered when it last
	// answered.
	checked, answered time.Time
	// asking is closed when the ask in flight ends, nil without one.
	asking chan struct{}
}

func newEdgeNotices() *edgeNotices {
	return &edgeNotices{by: map[string]*cachedNotice{}, now: time.Now}
}

// generation is the count a lookup's signed-in rows are read under.
func (n *edgeNotices) generation() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.gen
}

// lookup returns the notice of each of origins, the signed-in edges read
// under gen, that has one. When gen is current it forgets every other edge
// and asks, under life, each edge not asked within noticeRecheck. Callers
// share an ask in flight and wait for it at most noticeTimeout, and no
// longer than ctx.
func (n *edgeNotices) lookup(life, ctx context.Context, dir string, origins []string, gen uint64) map[string]edgeNotice {
	n.mu.Lock()
	current := gen == n.gen
	if current {
		for origin := range n.by {
			if !slices.Contains(origins, origin) {
				delete(n.by, origin)
			}
		}
	}
	now := n.now()
	var waits []chan struct{}
	for _, origin := range origins {
		entry := n.by[origin]
		if entry == nil && !current {
			continue
		}
		if entry == nil {
			entry = &cachedNotice{}
			n.by[origin] = entry
		}
		if current && entry.asking == nil && now.Sub(entry.checked) >= noticeRecheck {
			entry.checked, entry.asking = now, make(chan struct{})
			go n.ask(life, dir, origin, entry)
		}
		if entry.asking != nil {
			waits = append(waits, entry.asking)
		}
	}
	n.mu.Unlock()
	timeout := time.NewTimer(noticeTimeout)
	defer timeout.Stop()
wait:
	for _, done := range waits {
		select {
		case <-done:
		case <-timeout.C:
			break wait
		case <-ctx.Done():
			break wait
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[string]edgeNotice{}
	now = n.now()
	for _, origin := range origins {
		if entry := n.by[origin]; entry != nil && entry.notice != nil && now.Sub(entry.answered) <= noticeMaxAge {
			out[origin] = *entry.notice
		}
	}
	return out
}

func (n *edgeNotices) ask(life context.Context, dir, origin string, entry *cachedNotice) {
	ctx, cancel := context.WithTimeout(life, noticeTimeout)
	defer cancel()
	notice, err := askNotice(ctx, dir, origin)
	n.mu.Lock()
	defer n.mu.Unlock()
	close(entry.asking)
	entry.asking = nil
	if err == nil && n.by[origin] == entry {
		entry.notice, entry.answered = notice, n.now()
	}
}

// askNotice reads origin's notice, nil when it carries none. A refusal
// as retired is a notice too, and a 410 without a replacement clears it;
// any other failure is an error, which keeps what the cache holds.
func askNotice(ctx context.Context, dir, origin string) (*edgeNotice, error) {
	client, err := edgeclient.New(dir, origin)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	r, ok, err := client.RetiringNotice(ctx)
	var refused *edgeclient.RefusedError
	switch {
	case errors.As(err, &refused) && refused.Retired != "":
		if cli.SameEdge(origin, refused.Retired) {
			return nil, nil
		}
		return &edgeNotice{State: edgeproto.NoticeRetired, Replacement: refused.Retired}, nil
	case errors.As(err, &refused) && refused.Status == http.StatusGone:
		return nil, nil
	case err != nil:
		return nil, err
	case !ok || cli.SameEdge(origin, r.Replacement):
		return nil, nil
	}
	return &edgeNotice{State: edgeproto.NoticeRetiring, Replacement: r.Replacement, Date: r.Date}, nil
}

// forget drops what the cache holds for origin, after a sign-in or a
// logout there.
func (n *edgeNotices) forget(origin string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.by, origin)
	n.gen++
}

func (n *edgeNotices) reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	clear(n.by)
	n.gen++
}
