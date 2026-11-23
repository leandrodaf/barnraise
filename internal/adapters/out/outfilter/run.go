package outfilter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
)

// memCap is how much of the combined output is kept in memory for the
// summary; the log file always gets everything.
const memCap = 10 << 20

// Run executes command, writes the full combined output (stdout and stderr
// interleaved) to logDir/<timestamp>-<tool>.log and returns the summary.
// A non-zero exit code is not an error; err is set only when the command
// could not start, the log could not be written, or ctx ended.
func Run(ctx context.Context, command []string, logDir string) (summary string, exitCode int, logPath string, err error) {
	if len(command) == 0 {
		return "", -1, "", errors.New("outfilter: empty command")
	}
	tool := Detect(strings.Join(command, " "))
	if logDir == "" {
		logDir = os.TempDir()
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", -1, "", err
	}
	name := fmt.Sprintf("%s-%s.log", time.Now().UTC().Format("20060102T150405.000000000Z"), tool)
	logPath = filepath.Join(logDir, name)
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", -1, "", err
	}
	defer f.Close()

	mem := execx.NewTailBuffer(memCap)
	w := io.MultiWriter(f, mem)
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdout = w
	cmd.Stderr = w
	cmd.Stdin = os.Stdin
	execx.KillGroupOnCancel(cmd, 2*time.Second)
	if err := cmd.Start(); err != nil {
		return "", -1, logPath, fmt.Errorf("start %s: %w", command[0], err)
	}
	waitErr := cmd.Wait()
	exitCode = -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if waitErr != nil {
		if _, ok := errors.AsType[*exec.ExitError](waitErr); !ok {
			err = waitErr
		}
	}
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return FilterWithLog(tool, mem.Bytes(), exitCode, logPath), exitCode, logPath, err
}
