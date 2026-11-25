package linear

import (
	"context"
	"errors"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// Service answers each member from their own saved key.
type Service struct {
	Keys   *Keys
	Client *Client
}

func (s *Service) key(member domain.MemberID) (string, error) {
	key, err := s.Keys.Get(member)
	if errors.Is(err, ErrNotConnected) {
		return "", &protocol.Error{Code: protocol.CodeInvalidState,
			Message: "Linear is not connected: save a personal API key in Settings → Linear"}
	}
	return key, err
}

// Status reports the member's connection. Linear refusing the saved key
// is an error, not a disconnection: the member sees why and replaces it.
func (s *Service) Status(ctx context.Context, member domain.MemberID) (protocol.LinearStatusResult, error) {
	key, err := s.Keys.Get(member)
	if errors.Is(err, ErrNotConnected) {
		return protocol.LinearStatusResult{}, nil
	}
	if err != nil {
		return protocol.LinearStatusResult{}, err
	}
	return s.Client.Viewer(ctx, key)
}

// Connect saves the key only after Linear accepts it.
func (s *Service) Connect(ctx context.Context, member domain.MemberID, p protocol.LinearConnectParams) (protocol.LinearStatusResult, error) {
	key := strings.TrimSpace(p.APIKey)
	if key == "" {
		return protocol.LinearStatusResult{}, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "linear.connect: api_key is required"}
	}
	status, err := s.Client.Viewer(ctx, key)
	if err != nil {
		return protocol.LinearStatusResult{}, err
	}
	if err := s.Keys.Put(member, key); err != nil {
		return protocol.LinearStatusResult{}, err
	}
	return status, nil
}

func (s *Service) Disconnect(member domain.MemberID) error {
	if err := s.Keys.Delete(member); err != nil && !errors.Is(err, ErrNotConnected) {
		return err
	}
	return nil
}

func (s *Service) Teams(ctx context.Context, member domain.MemberID) (protocol.LinearTeamsResult, error) {
	key, err := s.key(member)
	if err != nil {
		return protocol.LinearTeamsResult{}, err
	}
	teams, err := s.Client.Teams(ctx, key)
	return protocol.LinearTeamsResult{Teams: teams}, err
}

func (s *Service) Issues(ctx context.Context, member domain.MemberID, p protocol.LinearIssuesParams) (protocol.LinearIssuesResult, error) {
	key, err := s.key(member)
	if err != nil {
		return protocol.LinearIssuesResult{}, err
	}
	issues, err := s.Client.Issues(ctx, key, p)
	return protocol.LinearIssuesResult{Issues: issues}, err
}

func (s *Service) Issue(ctx context.Context, member domain.MemberID, p protocol.LinearIssueParams) (protocol.LinearIssue, error) {
	identifier := strings.TrimSpace(p.Identifier)
	if identifier == "" {
		return protocol.LinearIssue{}, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "linear.issue: identifier is required"}
	}
	key, err := s.key(member)
	if err != nil {
		return protocol.LinearIssue{}, err
	}
	return s.Client.Issue(ctx, key, identifier)
}
