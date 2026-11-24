package checks

import (
	"context"
	"fmt"
	"os"

	"github.com/leandrodaf/barnraise/internal/adapters/out/astgrep"
	"github.com/leandrodaf/barnraise/internal/adapters/out/depscan"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Builtin runs a built-in check kind and returns its result and full log.
type Builtin func(ctx context.Context, spec ports.CheckSpec) (domain.CheckResult, []byte, error)

// defaultBuiltins are the built-in check kinds: the dependencies check and
// ast-grep.
func defaultBuiltins(o Options) map[string]Builtin {
	deps := depscan.New(depscan.Options{Exec: o.Exec, MainRefs: o.MainRefs})
	sg := astgrep.New(astgrep.Options{Exec: o.Exec, Machine: o.Machine})
	return map[string]Builtin{
		domain.RunsOnDependencies: func(ctx context.Context, spec ports.CheckSpec) (domain.CheckResult, []byte, error) {
			res, err := deps.Check(ctx, spec, o.Dependencies)
			return res, []byte(res.Summary + "\n"), err
		},
		domain.RunsOnASTGrep: sg.Check,
	}
}

// runBuiltin runs a check Barnraise implements itself, here, with the
// daemon's own network (the OSV API) and tools (osv-scanner, ast-grep).
func (r *Runner) runBuiltin(ctx context.Context, spec ports.CheckSpec, res *domain.CheckResult) error {
	run, ok := r.o.Builtin[spec.Def.RunsOn]
	if !ok {
		return fmt.Errorf("check %s: no built-in check kind %s", spec.Def.Name, spec.Def.RunsOn)
	}
	out, log, err := run(ctx, spec)
	if err != nil {
		return fmt.Errorf("check %s: %w", spec.Def.Name, err)
	}
	res.Passed, res.MainRed, res.Summary, res.Duration = out.Passed, out.MainRed, out.Summary, out.Duration
	if len(log) > 0 {
		p, err := r.saveLog(spec, "branch", 1, log)
		if err != nil {
			return err
		}
		res.LogPath = p
		r.keepLog(ctx, res)
	}
	return nil
}

// keepLog stores the log in the blob store, when there is one.
func (r *Runner) keepLog(ctx context.Context, res *domain.CheckResult) {
	if r.o.Blobs == nil || res.LogPath == "" {
		return
	}
	if f, err := os.Open(res.LogPath); err == nil {
		if h, err := r.o.Blobs.Put(ctx, f); err == nil {
			res.LogHash = h
		}
		f.Close()
	}
}
