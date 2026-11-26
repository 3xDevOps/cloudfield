package edgeclient

import (
	"context"
	"net/http"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// Retiring is an edge's notice that the address pair this client uses
// is retiring: Replacement is the relay origin to use instead, and Date,
// YYYY-MM-DD or empty, when the edge's operator says it ends. It is text
// to show; nothing signs it.
type Retiring struct {
	Replacement string
	Date        string
}

// Retiring is the notice of the edge's latest answer to this client,
// false when that answer carried none or none came yet.
func (c *Client) Retiring() (Retiring, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retiring == nil {
		return Retiring{}, false
	}
	return *c.retiring, true
}

// RetiringNotice asks the edge's relay origin for its metadata and
// returns the notice it carries, false when it carries none or one that
// is not a canonical origin and a real date.
func (c *Client) RetiringNotice(ctx context.Context) (Retiring, bool, error) {
	info, err := c.metadata(ctx)
	if err != nil || info.Retiring == "" {
		return Retiring{}, false, err
	}
	if clean(info.Retiring) != info.Retiring || !canonicalOrigin(info.Retiring) || (info.RetiringDate != "" && !edgeproto.ValidDate(info.RetiringDate)) {
		return Retiring{}, false, nil
	}
	return Retiring{Replacement: info.Retiring, Date: info.RetiringDate}, true, nil
}

func canonicalOrigin(origin string) bool {
	o, err := edgeproto.Origin(origin)
	return err == nil && o == origin
}

// observe records the notice of resp, an answer of the edge; an answer
// without the edge's protocol version came from something in between and
// says nothing.
func (c *Client) observe(resp *http.Response) {
	if resp.Header.Get(edgeproto.HeaderVersion) == "" {
		return
	}
	var notice *Retiring
	if r, ok := parseRetiring(resp.Header.Get(edgeproto.HeaderRetiring)); ok {
		notice = &r
	}
	c.mu.Lock()
	c.retiring = notice
	c.mu.Unlock()
}

func parseRetiring(value string) (Retiring, bool) {
	replacement, date, ok := edgeproto.ParseRetiring(value)
	if !ok || clean(replacement) != replacement {
		return Retiring{}, false
	}
	return Retiring{Replacement: replacement, Date: date}, true
}
