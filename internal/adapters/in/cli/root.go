// Package cli is the barnraise command line: short commands that ask the
// daemon through the public API, with human output or --json.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/client"
)

// Hooks are set by the composition root (cmd/barnraise).
type Hooks struct {
	Version string
	// RunDaemon runs the daemon in the foreground.
	RunDaemon func(ctx context.Context, opts DaemonOptions) error
}

// DaemonOptions are the daemon flags.
type DaemonOptions struct {
	Listen   string
	TLSCert  string
	TLSKey   string
	Machine  string
	NATSPort int
	NoAgents bool
	Fake     bool
	Egress   string // run the allow-list proxy on this address and send agent traffic through it
}

type env struct {
	hooks   Hooks
	project string
	json    bool
	url     string
	out     io.Writer
	errOut  io.Writer
	in      io.Reader
}

// Main runs the CLI and returns the process exit code.
func Main(args []string, h Hooks) int {
	e := &env{hooks: h, out: os.Stdout, errOut: os.Stderr, in: os.Stdin}
	root := e.rootCmd()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		if ec, ok := err.(exitCode); ok {
			return int(ec)
		}
		fmt.Fprintln(e.errOut, "barnraise:", client.Friendly(err))
		return 1
	}
	return 0
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func (e *env) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "barnraise",
		Short:         "Barnraise coordinates AI coding agents to drive a software project from start to finish",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       e.hooks.Version,
	}
	root.PersistentFlags().StringVarP(&e.project, "project", "p", os.Getenv("BARNRAISE_PROJECT"), "project name (default: the only one, or the one in the current directory)")
	root.PersistentFlags().BoolVar(&e.json, "json", false, "print JSON")
	root.PersistentFlags().StringVar(&e.url, "url", os.Getenv("BARNRAISE_URL"), "daemon URL (default: the local socket)")
	root.AddGroup(&cobra.Group{ID: "work", Title: "Work"}, &cobra.Group{ID: "owner", Title: "Owner"}, &cobra.Group{ID: "know", Title: "Knowledge"},
		&cobra.Group{ID: "setup", Title: "Setup"}, &cobra.Group{ID: "agent", Title: "Used by agents and hooks"})
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("work", e.statusCmd(), e.overviewCmd(), e.itemCmd(), e.queueCmd(), e.proposalsCmd(), e.proposeCmd(), e.tweakCmd(), e.agentsCmd(), e.metricsCmd(), e.digestCmd(), e.eventsCmd(), e.evalCmd())
	add("owner", e.approveCmd(true), e.approveCmd(false), e.answerCmd(), e.questionsCmd(), e.goalCmd(), e.focusCmd(), e.pauseCmd(), e.resumeCmd(), e.standbyCmd(), e.takeoverCmd(), e.handbackCmd(), e.chatCmd(), e.trustCmd())
	add("owner", e.lessonCmd())
	add("know", e.contextCmd(), e.findCmd(), e.knowCmd("get", "Get the full content of an id (I-142, D-12, a file or a symbol)"),
		e.knowCmd("related", "Show what is linked to an id"), e.knowCmd("why", "Explain why a line is the way it is (path:line)"),
		e.knowCmd("history", "Show what happened to a symbol or path"), e.knowCmd("rules", "List the rules that apply to a path"), e.revalidateCmd())
	add("setup", e.initCmd(), e.upCmd(), e.daemonCmd(), e.checkCmd(), e.explainCmd(), e.templateCmd(), e.accountCmd(), e.trackerCmd(), e.webhookCmd(), e.machineCmd(), e.clusterCmd(), e.tokenCmd(), e.auditCmd(), e.downCmd())
	add("agent", e.imageCmd(), e.firewallCmd(), e.nextCmd(), e.deliverCmd(), e.reviewCmd(), e.askCmd(), e.messageCmd(), e.inboxCmd(), e.journalCmd(), e.hookCmd(), e.statuslineCmd(), e.filterCmd(), e.outputCmd(), e.mcpCmd())
	add("setup", e.sandboxCmd())
	root.AddCommand(e.sandboxExecCmd())
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print the version", Run: func(*cobra.Command, []string) { fmt.Fprintln(e.out, e.hooks.Version) }})
	return root
}

func (e *env) client() *client.Client {
	o := client.FromEnv()
	if e.url != "" {
		o.URL = e.url
	}
	return client.New(o)
}

func (e *env) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

// proj resolves the project: flag, env, or the project whose root is the
// current directory (the daemon knows them), or the only one.
func (e *env) proj(ctx context.Context) string {
	if e.project != "" {
		return e.project
	}
	if root, err := findRoot(); err == nil {
		if name := projectNameAt(root); name != "" {
			return name
		}
	}
	return ""
}

func req[T any](m *T) *connect.Request[T] { return connect.NewRequest(m) }

var pbJSON = protojson.MarshalOptions{UseProtoNames: true, Multiline: true, Indent: "  "}

// print writes a proto message as JSON (with --json) or uses the human printer.
func (e *env) print(m proto.Message, human func(w io.Writer)) {
	if e.json || human == nil {
		data, err := pbJSON.Marshal(m)
		if err != nil {
			fmt.Fprintln(e.errOut, err)
			return
		}
		fmt.Fprintln(e.out, string(data))
		return
	}
	human(e.out)
}

func (e *env) printAny(v any) {
	data, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(e.out, string(data))
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", v) }

func when(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil || t.IsZero() || t.Year() < 2000 {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "in " + (-d).Round(time.Minute).String()
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return t.Local().Format("2006-01-02")
}

func itemLine(it *pb.Item) string {
	holder := ""
	if it.Claim != nil && it.Claim.Machine != "" {
		holder = " @" + it.Claim.Machine
	}
	step := ""
	if it.Step != "" {
		step = " [" + it.Step + "]"
	}
	area := ""
	if it.Area != "" {
		area = " A-" + it.Area
	}
	return fmt.Sprintf("%-7s P%d %-13s %s%s%s%s", it.Id, it.Priority, it.Status, it.Title, step, area, holder)
}

func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
