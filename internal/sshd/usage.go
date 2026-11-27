package sshd

import (
	"context"
	"encoding/json"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/fallback"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/quota"
)

func init() {
	registerMethod(protocol.MethodAccountUsage, (*Server).accountUsage)
}

func (s *Server) accountUsage(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	params, perr := decodeParams[protocol.AccountUsageParams](raw)
	if perr != nil {
		return nil, perr
	}
	if err := s.checkMember(ctx, member); err != nil {
		return nil, rpcError(err)
	}
	account, perr := s.launchAccount(ctx, member, params.AccountMemberID)
	if perr != nil {
		return nil, perr
	}
	reader := s.cfg.Services.Usage
	if reader == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "account.usage: usage service not configured"}
	}
	login, perr := s.usageLogin(ctx, member, account, params.Login)
	if perr != nil {
		return nil, perr
	}

	var (
		providers []quota.Provider
		err       error
	)
	if login.Added() {
		providers, err = reader.(fallback.LoginReader).ReadLogin(ctx, domain.Account{Member: account, Login: login}, params.Refresh)
	} else {
		providers, err = reader.Read(ctx, account, params.Refresh)
	}
	if err != nil {
		// Provider implementations return safe per-provider diagnostics in
		// their rows. A top-level error may wrap an HTTP body or credential
		// path, so never echo it over the RPC boundary.
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "account.usage: usage providers unavailable"}
	}

	// The account grant and caller membership are checked again after the
	// potentially slow provider read. A revoke or removal during the read
	// must not turn a private result into a successful response.
	if err := s.checkMember(ctx, member); err != nil {
		return nil, rpcError(err)
	}
	if _, perr := s.launchAccount(ctx, member, string(account)); perr != nil {
		return nil, perr
	}
	if _, perr := s.usageLogin(ctx, member, account, params.Login); perr != nil {
		return nil, perr
	}

	out := protocol.AccountUsageResult{
		AccountMemberID: string(account),
		Login:           params.Login,
		Providers:       make([]protocol.UsageProvider, 0, len(providers)),
	}
	for _, provider := range providers {
		row := protocol.UsageProvider{
			Provider:  provider.Provider,
			Status:    provider.Status,
			Windows:   make([]protocol.UsageWindow, 0, len(provider.Windows)),
			Plan:      provider.Plan,
			CheckedAt: provider.CheckedAt.UTC().Format(time.RFC3339),
			Error:     provider.Error,
		}
		if provider.UpdatedAt != nil {
			updated := provider.UpdatedAt.UTC().Format(time.RFC3339)
			row.UpdatedAt = &updated
		}
		if provider.RetryAt != nil {
			retry := provider.RetryAt.UTC().Format(time.RFC3339)
			row.RetryAt = &retry
		}
		for _, window := range provider.Windows {
			wireWindow := protocol.UsageWindow{
				ID:          window.ID,
				Label:       window.Label,
				UsedPercent: window.UsedPercent,
			}
			if window.ResetsAt != nil {
				resets := window.ResetsAt.UTC().Format(time.RFC3339)
				wireWindow.ResetsAt = &resets
			}
			row.Windows = append(row.Windows, wireWindow)
		}
		out.Providers = append(out.Providers, row)
	}
	return out, nil
}

// usageLogin resolves the login account.usage reads: one of the caller's
// own logins of Claude Code. A share reaches its owner's default login only,
// so a login named on another member's account is refused.
func (s *Server) usageLogin(ctx context.Context, member, account domain.MemberID, login string) (domain.LoginID, *protocol.Error) {
	id := domain.LoginID(login)
	if !id.Added() {
		return "", nil
	}
	if account != member {
		return "", &protocol.Error{Code: protocol.CodeDenied, Message: "account.usage: a login is named only on your own account; a share reaches its owner's default login"}
	}
	if _, ok := s.cfg.Services.Usage.(fallback.LoginReader); !ok {
		return "", errNoAddedLogins
	}
	logins, err := s.cfg.Store.ListAgentLogins(ctx, member, "claude")
	if err != nil {
		return "", rpcError(err)
	}
	if _, perr := addedLogin(logins, login); perr != nil {
		return "", perr
	}
	return id, nil
}
