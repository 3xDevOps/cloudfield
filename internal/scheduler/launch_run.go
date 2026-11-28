package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/gitengine"
	"github.com/3xDevOps/cloudfield/internal/mirror"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// launchRun captures the base of a launch and returns the row it creates.
func (s *Scheduler) launchRun(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, acp, perUse bool, extraRepos []domain.RunRepo, opts domain.LaunchOptions) (*domain.Run, error) {
	s.mu.Lock()
	bases := s.cfg.Bases
	s.mu.Unlock()
	if bases == nil {
		return nil, errors.New("scheduler: base capture is not configured")
	}
	var err error
	var extrasCh chan extraCapture
	if len(opts.ExtraWorkspaces) > 0 {
		extrasCh = make(chan extraCapture, 1)
		go func() {
			repos, captureErr := s.captureExtras(ctx, bases, extraRepos)
			extrasCh <- extraCapture{repos, captureErr}
		}()
	}
	var base mirror.CaptureResult
	if opts.FromCommit != "" {
		base = mirror.CaptureResult{Commit: opts.FromCommit, Branch: opts.FromBranch, Source: "run", Configured: true, CheckedAt: time.Now().UTC()}
	} else {
		base, err = bases.Capture(ctx, workspace, opts.CachedBase)
	}
	var extras extraCapture
	if extrasCh != nil {
		extras = <-extrasCh
	}
	runtime.Step(ctx, "base")
	if err != nil {
		return nil, &BaseCaptureError{Capture: base, Err: err}
	}
	if extras.err != nil {
		return nil, extras.err
	}
	if opts.CachedBase != "" && (!base.Cached || base.Commit != opts.CachedBase) {
		return nil, &BaseCaptureError{
			Capture: base,
			Err: &gitengine.MirrorError{
				Kind:        gitengine.MirrorErrorInvalidRequest,
				WorkspaceID: workspace,
				Base:        opts.CachedBase,
				Observed:    base.Commit,
			},
		}
	}
	source := base.Source
	switch {
	case base.Cached && source != "":
		source = "cached:" + source
	case base.Cached:
		source = "cached"
	case !base.Configured:
		source = "local"
	}
	run := &domain.Run{
		WorkspaceID:     workspace,
		MemberID:        member,
		AccountMemberID: account,
		Task:            task,
		Harness:         harness,
		Mode:            mode,
		Model:           opts.Model,
		Effort:          opts.Effort,
		Permission:      opts.Permission,
		Limits:          opts.Limits,
		Via:             domain.ViaFrom(ctx),
		ACP:             acp,
		Status:          domain.RunQueued,
		BaseCommit:      base.Commit,
		BaseBranch:      base.Branch,
		BaseSource:      source,
		BaseCheckedAt:   base.CheckedAt,
		Fallback:        opts.Fallback,
		AccountLogin:    opts.AccountLogin,
		Extras:          extras.repos,
		LinearIssueURL:  opts.LinearIssueURL,
	}
	run.AccountLoginPerUse = perUse
	if opts.RequestedAccount != account {
		run.RequestedAccountMemberID = opts.RequestedAccount
	}
	return run, nil
}
