package app

import (
	"context"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// updateFailures remembers the last self-update failure this machine told
// the cluster, so a failure repeating every few seconds is said once.
var updateFailures struct {
	mu   sync.Mutex
	last string
	at   time.Time
}

// restateUpdateFailure is how often the same self-update failure is told again.
const restateUpdateFailure = 30 * time.Minute

// NoteUpdateFailed tells every project, and the owner, that this machine
// could not take the newer version another machine runs. Only this
// machine's own log knew it before, so the cluster kept two versions
// with nobody aware.
func (s *Service) NoteUpdateFailed(ctx context.Context, from, version string, cause error) {
	if cause == nil {
		return
	}
	why := cause.Error()
	updateFailures.mu.Lock()
	if why == updateFailures.last && time.Since(updateFailures.at) < restateUpdateFailure {
		updateFailures.mu.Unlock()
		return
	}
	updateFailures.last, updateFailures.at = why, time.Now()
	updateFailures.mu.Unlock()
	note := "machine " + s.d.Machine + " cannot update to " + version + " from " + from + ": " + truncate(why, 400)
	extra := map[string]any{"machine": s.d.Machine, "from": from, "version": version, "current": s.d.Version, "error": truncate(why, 400)}
	for i, name := range s.Projects() {
		p, err := s.Project(name)
		if err != nil {
			continue
		}
		if _, err := s.emit(ctx, p, domain.EvMachineUpdateFailed, s.d.Machine, "barnraise", domain.Payload{Note: note, Extra: extra}); err != nil {
			s.d.Log.Warn("update failure not recorded", "project", name, "err", err)
		}
		if i == 0 {
			s.notifyOwner(ctx, p, "update_failed", "Machine "+s.d.Machine+" cannot update", note)
		}
	}
}
