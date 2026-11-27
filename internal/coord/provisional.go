package coord

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/store"
)

const (
	provisionalHeader = "Provisional report: this session ended its turn without `aether-internal report`. " +
		"A report from it supersedes this one. Its last message:\n\n"
	// provisionalStateHeader takes the outcome the turn reported.
	provisionalStateHeader = "Provisional report: this session ended its turn after reporting %s. That report is not forwarded; " +
		"a success, failure or blocked report from it supersedes this one. Its last message:\n\n"
)

// ProvisionalReport forwards a child run's last message to its parent when
// the turn that started at since ended without a report its parent is
// sent: none at all, or only needs-input, waiting or answered, which the
// message then names. The turn start keys the message, so each turn sends
// at most one.
func (s *Service) ProvisionalReport(ctx context.Context, run domain.RunID, since time.Time, message string) error {
	if s.cfg.Disabled {
		return nil
	}
	if s.cfg.Children == nil {
		return nil
	}
	recipient, err := s.cfg.Children.ReportRecipient(ctx, run)
	if err != nil {
		return fmt.Errorf("provisional report recipient: %w", err)
	}
	if recipient == "" {
		return nil
	}
	target, err := s.cfg.Store.GetRun(ctx, recipient)
	if err != nil {
		return fmt.Errorf("provisional report parent: %w", err)
	}
	// Under the report lock a report either is already reserved or forwards
	// after this message and retires it.
	lock := s.reportLock(run)
	lock.Lock()
	defer lock.Unlock()
	reported, err := s.cfg.Mail.CoordReportedSince(ctx, run, since)
	if err != nil || reported {
		return err
	}
	header := provisionalHeader
	state, err := s.cfg.Mail.CoordStateReportedSince(ctx, run, since)
	if err != nil {
		return fmt.Errorf("provisional report: %w", err)
	}
	if state != "" {
		header = fmt.Sprintf(provisionalStateHeader, state)
	}
	if message == "" {
		message = "(none)"
	}
	if room := protocol.CoordMaxBodyBytes - len(header); len(message) > room {
		message = strings.ToValidUTF8(message[:room], "")
	}
	turn := strconv.FormatInt(since.UnixMicro(), 10)
	created, err := appendRunMessage(ctx, s.cfg.Mail, &store.RunMessage{
		WorkspaceID: target.WorkspaceID, FromRun: run, ToRun: target.ID,
		Kind: store.RunMessageKindMessage, Body: header + message,
		CorrelationID:  store.ProvisionalReportPrefix + turn,
		IdempotencyKey: "coord-provisional:" + turn + ":" + string(target.ID),
	})
	if err != nil {
		return fmt.Errorf("enqueue provisional report: %w", err)
	}
	if created {
		s.wakeInbox(target.ID)
	}
	return nil
}
