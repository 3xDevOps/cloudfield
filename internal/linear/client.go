package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// APIURL is Linear's GraphQL endpoint.
const APIURL = "https://api.linear.app/graphql"

const maxResponse = 4 << 20

// Client calls Linear's GraphQL API with a personal API key.
type Client struct {
	URL  string
	HTTP *http.Client
}

func NewClient() *Client {
	return &Client{URL: APIURL, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Viewer returns who key belongs to; Linear refuses a key it does not know.
func (c *Client) Viewer(ctx context.Context, key string) (protocol.LinearStatusResult, error) {
	var data struct {
		Viewer struct {
			Name         string `json:"name"`
			Organization struct {
				Name   string `json:"name"`
				URLKey string `json:"urlKey"`
			} `json:"organization"`
		} `json:"viewer"`
	}
	if err := c.do(ctx, key, `query { viewer { name organization { name urlKey } } }`, nil, &data); err != nil {
		return protocol.LinearStatusResult{}, err
	}
	return protocol.LinearStatusResult{
		Connected:    true,
		Name:         data.Viewer.Name,
		Organization: data.Viewer.Organization.Name,
		URLKey:       data.Viewer.Organization.URLKey,
	}, nil
}

func (c *Client) Teams(ctx context.Context, key string) ([]protocol.LinearTeam, error) {
	var data struct {
		Teams struct {
			Nodes []protocol.LinearTeam `json:"nodes"`
		} `json:"teams"`
	}
	if err := c.do(ctx, key, `query { teams { nodes { key name } } }`, nil, &data); err != nil {
		return nil, err
	}
	return data.Teams.Nodes, nil
}

type issueNode struct {
	Identifier string                   `json:"identifier"`
	Title      string                   `json:"title"`
	URL        string                   `json:"url"`
	State      protocol.LinearState     `json:"state"`
	Team       *protocol.LinearTeam     `json:"team"`
	Assignee   *protocol.LinearAssignee `json:"assignee"`
	UpdatedAt  string                   `json:"updatedAt"`
}

func (n issueNode) issue() protocol.LinearIssue {
	return protocol.LinearIssue{
		Identifier: n.Identifier, Title: n.Title, URL: n.URL, State: n.State,
		Team: n.Team, Assignee: n.Assignee, UpdatedAt: n.UpdatedAt,
	}
}

// Issues returns the first 50 issues assigned to key's owner that are
// neither completed nor canceled, most recently updated first.
func (c *Client) Issues(ctx context.Context, key string, p protocol.LinearIssuesParams) ([]protocol.LinearIssue, error) {
	filter := map[string]any{"state": map[string]any{"type": map[string]any{"nin": []string{"completed", "canceled"}}}}
	if team := strings.TrimSpace(p.Team); team != "" {
		filter["team"] = map[string]any{"key": map[string]any{"eqIgnoreCase": team}}
	}
	if q := strings.TrimSpace(p.Query); q != "" {
		filter["title"] = map[string]any{"containsIgnoreCase": q}
	}
	var data struct {
		Viewer struct {
			AssignedIssues struct {
				Nodes []issueNode `json:"nodes"`
			} `json:"assignedIssues"`
		} `json:"viewer"`
	}
	const query = `query Issues($filter: IssueFilter) { viewer { assignedIssues(filter: $filter, first: 50, orderBy: updatedAt) {
		nodes { identifier title url state { name type color } team { key name } assignee { name } updatedAt } } } }`
	if err := c.do(ctx, key, query, map[string]any{"filter": filter}, &data); err != nil {
		return nil, err
	}
	issues := make([]protocol.LinearIssue, 0, len(data.Viewer.AssignedIssues.Nodes))
	for _, n := range data.Viewer.AssignedIssues.Nodes {
		issues = append(issues, n.issue())
	}
	return issues, nil
}

// Issue reads one issue by identifier, such as CLO-6.
func (c *Client) Issue(ctx context.Context, key, identifier string) (protocol.LinearIssue, error) {
	var data struct {
		Issue issueNode `json:"issue"`
	}
	const query = `query Issue($id: String!) { issue(id: $id) { identifier title url state { name type color } } }`
	if err := c.do(ctx, key, query, map[string]any{"id": identifier}, &data); err != nil {
		return protocol.LinearIssue{}, err
	}
	return data.Issue.issue(), nil
}

// do posts one GraphQL operation. Linear reports a refused key or a bad
// query in the errors array, usually with a 4xx status and sometimes with
// 200, so the array is read first and its messages are returned as Linear
// wrote them.
func (c *Client) do(ctx context.Context, key, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("linear: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("linear: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("linear: read response: %w", err)
	}
	var reply struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	decodeErr := json.Unmarshal(raw, &reply)
	if len(reply.Errors) > 0 {
		msgs := make([]string, len(reply.Errors))
		for i, e := range reply.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("linear: %s", strings.Join(msgs, "; "))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if decodeErr != nil {
		return fmt.Errorf("linear: decode response: %w", decodeErr)
	}
	if err := json.Unmarshal(reply.Data, out); err != nil {
		return fmt.Errorf("linear: decode data: %w", err)
	}
	return nil
}
