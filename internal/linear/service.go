package linear

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Service answers each member from their own saved key. A call that needs
// a key returns ErrNotConnected when the member has saved none.
type Service struct {
	Keys   *Keys
	Client *Client
}

// Status reports the member's connection. Linear refusing the saved key
// is an error, not a disconnection: the member sees why and replaces it.
func (s *Service) Status(ctx context.Context, member domain.MemberID) (Status, error) {
	key, err := s.Keys.Get(member)
	if errors.Is(err, ErrNotConnected) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	return s.Client.Viewer(ctx, key)
}

// Connect saves key only after Linear accepts it.
func (s *Service) Connect(ctx context.Context, member domain.MemberID, key string) (Status, error) {
	status, err := s.Client.Viewer(ctx, key)
	if err != nil {
		return Status{}, err
	}
	if err := s.Keys.Put(member, key); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (s *Service) Disconnect(member domain.MemberID) error {
	if err := s.Keys.Delete(member); err != nil && !errors.Is(err, ErrNotConnected) {
		return err
	}
	return nil
}

func (s *Service) Teams(ctx context.Context, member domain.MemberID) ([]Team, error) {
	key, err := s.Keys.Get(member)
	if err != nil {
		return nil, err
	}
	return s.Client.Teams(ctx, key)
}

func (s *Service) Issues(ctx context.Context, member domain.MemberID, f IssuesFilter) ([]Issue, error) {
	key, err := s.Keys.Get(member)
	if err != nil {
		return nil, err
	}
	return s.Client.Issues(ctx, key, f)
}

func (s *Service) Issue(ctx context.Context, member domain.MemberID, identifier string) (Issue, error) {
	key, err := s.Keys.Get(member)
	if err != nil {
		return Issue{}, err
	}
	return s.Client.Issue(ctx, key, identifier)
}
