package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// A machine that cannot take the newer version says so to the cluster and
// the owner, once, instead of only in its own log.
func TestUpdateFailureIsToldOnce(t *testing.T) {
	e := newEnv(t)
	err := errors.New("fetch binary 244a: object not found")
	e.svc.NoteUpdateFailed(e.ctx, "leandro-ferreira", "0.1.1-55", err)
	e.svc.NoteUpdateFailed(e.ctx, "leandro-ferreira", "0.1.1-55", err)
	n := 0
	for _, ty := range strings.Fields(e.types(e.svc.d.Machine)) {
		if ty == domain.EvMachineUpdateFailed {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d update failure events, want 1", n)
	}
	if k := strings.Join(e.notes.Kinds(), ","); !strings.Contains(k, "update_failed") {
		t.Fatalf("notifications %s", k)
	}
}
