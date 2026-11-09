package runmode

import (
	"context"
	"errors"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/claudeauth"
)

// ClaudeRefreshEvery is how often a machine with a Claude Code agent re-reads
// the token (it changes only when the operator replaces it).
const ClaudeRefreshEvery = 15 * time.Minute

// KeepClaudeToken refreshes the stored Claude credentials while the run lasts.
// The next Claude Code start uses a changed token; running agents keep theirs.
func (s *Service) KeepClaudeToken(ctx context.Context) {
	if _, err := claudeauth.Load(s.Home); errors.Is(err, claudeauth.ErrMissing) {
		return // no Claude Code agent on this run
	}
	ticker := time.NewTicker(ClaudeRefreshEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.refreshClaude(ctx); err != nil && s.OnError != nil {
				s.OnError(err)
			}
		}
	}
}

func (s *Service) refreshClaude(ctx context.Context) error {
	fresh, err := s.API.ClaudeToken(ctx)
	if err != nil {
		return err
	}
	current, err := claudeauth.Load(s.Home)
	if err == nil && *current == fresh {
		return nil
	}
	return claudeauth.Save(s.Home, fresh)
}
