package outfilter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// The kept file holds the first keepHead bytes and the last keepTail bytes
// of an output; the summary reads the last keepTail bytes. Variables for
// the tests.
var (
	keepHead int64 = 8 << 20
	keepTail       = 8 << 20
)

// Options of RunCommand.
type Options struct {
	// Store keeps the full text of every shortened output.
	Store Store
	// Delegate is the outfilter.delegate mode: auto, rtk or off.
	Delegate string
	// RTK is the rtk binary; empty looks it up on the PATH.
	RTK string
	// Run is the agent session's run id, written in the record.
	Run string
	// Raw prints the whole output (it is still kept).
	Raw bool
	// Stdin is the command's input (nil: none).
	Stdin io.Reader
}

// Result of RunCommand.
type Result struct {
	Text      string // what to print
	Exit      int
	Tool      string
	Record    domain.OutputRecord // set when the output was kept (Record.ID != "")
	Shortened bool
}

// RunCommand executes command, prints its output filtered, and keeps the
// full text in the store when what it prints is shorter, ending the text
// with the handle to read the rest. A non-zero exit code is not an error;
// err is set only when the command could not start, the store failed, or
// ctx ended.
func RunCommand(ctx context.Context, command []string, o Options) (Result, error) {
	if len(command) == 0 {
		return Result{Exit: -1}, errors.New("outfilter: empty command")
	}
	cmdline := strings.Join(command, " ")
	if len(command) == 3 && command[1] == "-c" && (command[0] == "sh" || command[0] == "bash") {
		cmdline = command[2]
	}
	tool := Detect(cmdline)
	if o.Store.Dir == "" {
		o.Store.Dir = os.TempDir()
	}
	if err := os.MkdirAll(o.Store.Dir, 0o700); err != nil {
		return Result{Exit: -1}, err
	}
	argv, delegated := command, ""
	if rtk := o.rtkFor(tool, cmdline); rtk != "" {
		argv, delegated = append([]string{rtk}, strings.Fields(cmdline)...), "rtk"
	}
	out, err := runKept(ctx, argv, o)
	if err == nil && delegated != "" && rtkRefused(out) {
		// rtk does not know this command and did not run it: run it as is.
		out.discard()
		argv, delegated = command, ""
		out, err = runKept(ctx, argv, o)
	}
	if err != nil {
		if out != nil {
			out.discard()
		}
		return Result{Exit: out.exitCode(), Tool: tool}, err
	}
	res := Result{Exit: out.exit, Tool: tool}
	id := newID()
	footer := fmt.Sprintf("full output: barnraise output show %s (%d lines; --grep <re> or --lines a:b for a part)", id, out.lines)
	var text string
	shortened := false
	switch {
	case o.Raw:
		text = strings.Join(splitLines(out.tail.Bytes()), "\n")
	case delegated != "":
		text, shortened = Summarize(ToolLogs, out.tail.Bytes(), out.exit, footer)
		text += "\n(filtered by rtk)"
	default:
		text, shortened = Summarize(tool, out.tail.Bytes(), out.exit, footer)
	}
	if shortened && !o.Raw && int64(len(text)) >= out.total {
		// The summary is no shorter than the output: print the output.
		text, shortened = strings.Join(splitLines(out.tail.Bytes()), "\n"), false
	}
	res.Text, res.Shortened = text, shortened
	if !shortened {
		out.discard()
		return res, nil
	}
	if err := os.Rename(out.file.Name(), o.Store.Path(id)); err != nil {
		out.discard()
		return res, err
	}
	rec := domain.OutputRecord{ID: id, Run: o.Run, At: o.Store.now(), Tool: tool, Command: truncateRunes(cmdline, 300), Exit: out.exit,
		Bytes: out.kept, RawTokens: int((out.total + 3) / 4), ShownTokens: domain.EstimateTokens(text), Delegated: delegated, Truncated: out.truncated}
	res.Record = rec
	return res, o.Store.add(rec)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// keptOutput is a command's combined output on its way to the store.
type keptOutput struct {
	file      *os.File
	tail      *execx.TailBuffer
	written   int64 // bytes of the head in the file
	total     int64
	kept      int64
	lines     int
	truncated bool
	exit      int
	werr      error
}

func (k *keptOutput) Write(p []byte) (int, error) {
	k.total += int64(len(p))
	k.lines += bytes.Count(p, []byte{'\n'})
	_, _ = k.tail.Write(p)
	if k.written < keepHead && k.werr == nil {
		n := min(int64(len(p)), keepHead-k.written)
		if _, err := k.file.Write(p[:n]); err != nil {
			k.werr = err
		}
		k.written += n
	}
	return len(p), nil
}

// finish completes the file with the part of the tail it does not hold.
func (k *keptOutput) finish() error {
	if rest := k.total - k.written; rest > 0 && k.werr == nil {
		tb := k.tail.Bytes()
		if rest <= int64(len(tb)) {
			_, k.werr = k.file.Write(tb[int64(len(tb))-rest:])
		} else {
			k.truncated = true
			if _, err := fmt.Fprintf(k.file, "\n… %d bytes omitted to keep the output bounded …\n", rest-int64(len(tb))); err != nil {
				k.werr = err
			} else {
				_, k.werr = k.file.Write(tb)
			}
		}
	}
	if st, err := k.file.Stat(); err == nil {
		k.kept = st.Size()
	}
	if cerr := k.file.Close(); cerr != nil && k.werr == nil {
		k.werr = cerr
	}
	if k.total > 0 && !strings.HasSuffix(string(k.tail.Bytes()), "\n") {
		k.lines++
	}
	return k.werr
}

func (k *keptOutput) discard() {
	_ = k.file.Close()
	_ = os.Remove(k.file.Name())
}

func (k *keptOutput) exitCode() int {
	if k == nil {
		return -1
	}
	return k.exit
}

func runKept(ctx context.Context, argv []string, o Options) (*keptOutput, error) {
	f, err := os.CreateTemp(o.Store.Dir, ".run-*.tmp")
	if err != nil {
		return nil, err
	}
	k := &keptOutput{file: f, tail: execx.NewTailBuffer(keepTail), exit: -1}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = k
	cmd.Stderr = k
	cmd.Stdin = o.Stdin
	execx.KillGroupOnCancel(cmd, 2*time.Second)
	if err := cmd.Start(); err != nil {
		return k, fmt.Errorf("start %s: %w", argv[0], err)
	}
	waitErr := cmd.Wait()
	if cmd.ProcessState != nil {
		k.exit = cmd.ProcessState.ExitCode()
	}
	ferr := k.finish()
	switch {
	case ctx.Err() != nil:
		return k, ctx.Err()
	case waitErr != nil:
		if _, ok := errors.AsType[*exec.ExitError](waitErr); !ok {
			return k, waitErr
		}
	}
	return k, ferr
}
