package cli

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/app"
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/domain"
)

func (e *env) documentsCmd() *cobra.Command {
	c := &cobra.Command{Use: "documents", Aliases: []string{"docs"},
		Short: "The texts Barnraise writes (pull request, plan, item, issue): list, customize, preview, check",
		Long: "Barnraise writes the body of each pull request, the structure of plan.md, the body of an item made from a proposal and of its issue " +
			"in the tracker from templates. Each has a built-in version; a project replaces it with .barnraise/documents/<name>.md, or for one area or " +
			"kind with <name>.<area>.md or <name>.<kind>.md. Templates are Go text/template; `barnraise documents show <name>` prints one and lists what it may use.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			srcs := app.DocumentSources(root)
			if e.json {
				e.printAny(srcs)
				return nil
			}
			for _, s := range srcs {
				from := "built-in"
				if s.Path != "" {
					from = s.Path
				}
				fmt.Fprintf(e.out, "%-6s %s\n", s.Name, from)
				for _, v := range s.Variants {
					fmt.Fprintf(e.out, "       + %s\n", v)
				}
			}
			cfg, err := config.Load(root)
			if err == nil {
				fmt.Fprintf(e.out, "\nDiagrams in plans: %s (documents.diagrams: auto, always or off)\n", cmp.Or(cfg.Documents.Diagrams, domain.DiagramsAuto))
			}
			fmt.Fprintln(e.out, "Customize one with: barnraise documents customize <name> [--area <area> | --kind <kind>]")
			return nil
		}}
	c.AddCommand(e.documentsShowCmd(), e.documentsCustomizeCmd(), e.documentsPreviewCmd(), e.documentsCheckCmd())
	return c
}

func docName(n string) (string, error) {
	n = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(n)), ".md")
	if !slices.Contains(domain.DocumentNames, n) {
		return "", fmt.Errorf("unknown document %q (known: %s)", n, strings.Join(domain.DocumentNames, ", "))
	}
	return n, nil
}

const docFieldsHelp = `Available in every template:
  .Project .Language .DiagramsMode (auto|always|off) .Default (the built-in text)
  .Item.ID .Item.Title .Item.Kind .Item.Priority .Item.Area .Item.Goal .Item.TrackerRef
  .Item.Step .Item.Status .Item.Body .Item.Branch .Item.PR
  .Criteria (checklist)  .Preserve .Tweaks (lists)
  .Plan (plan.md)  .Diagrams (each .Title and .Source, the Mermaid blocks of plan.md)
  .Proposal.ID .Proposal.Problem .Proposal.Evidence .Proposal.Impact .Proposal.Goal (item only)
Functions: bullets LIST · mermaid DIAGRAM · section .Plan "Risks" · join LIST ", " · trim · lower · upper · default "x" VALUE`

func (e *env) documentsShowCmd() *cobra.Command {
	var builtIn bool
	var area, kind string
	c := &cobra.Command{Use: "show <name>", Short: "Print the template of a document in use (or the built-in one)", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			n, err := docName(args[0])
			if err != nil {
				return err
			}
			var text []byte
			src := "built-in"
			if !builtIn {
				root, err := findRoot()
				if err != nil {
					return err
				}
				if f := app.DocumentFile(root, n, area, kind); f != "" {
					text, err = os.ReadFile(f)
					if err != nil {
						return err
					}
					src = relPath(root, f)
				}
			}
			if text == nil {
				b, err := domain.BuiltInDocument(n)
				if err != nil {
					return err
				}
				text = []byte(b)
			}
			fmt.Fprintf(e.errOut, "# %s (%s)\n", n, src)
			fmt.Fprint(e.out, string(text))
			fmt.Fprintln(e.errOut, "\n"+docFieldsHelp)
			return nil
		}}
	c.Flags().BoolVar(&builtIn, "built-in", false, "the built-in template, even when the project has its own")
	c.Flags().StringVar(&area, "area", "", "the template an item of this area uses")
	c.Flags().StringVar(&kind, "kind", "", "the template an item of this kind uses")
	return c
}

func (e *env) documentsCustomizeCmd() *cobra.Command {
	var area, kind string
	var force bool
	c := &cobra.Command{Use: "customize <name>", Aliases: []string{"edit"},
		Short: "Copy the built-in template of a document into .barnraise/documents/ to change it", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			n, err := docName(args[0])
			if err != nil {
				return err
			}
			if area != "" && kind != "" {
				return errors.New("choose --area or --kind, not both")
			}
			root, err := findRoot()
			if err != nil {
				return err
			}
			file := n + ".md"
			if v := strings.TrimSpace(cmp.Or(area, kind)); v != "" {
				if strings.ContainsAny(v, `/\. `) {
					return fmt.Errorf("invalid area or kind %q", v)
				}
				file = n + "." + v + ".md"
			}
			dst := filepath.Join(root, filepath.FromSlash(domain.DocumentsDir), file)
			if _, err := os.Stat(dst); err == nil && !force {
				return fmt.Errorf("%s exists; edit it, or pass --force to start again from the built-in one", relPath(root, dst))
			}
			// Start from the template in use (the project's general one for a variant), else the built-in one.
			builtIn, err := domain.BuiltInDocument(n)
			if err != nil {
				return err
			}
			b := []byte(builtIn)
			if file != n+".md" {
				if own, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(domain.DocumentsDir), n+".md")); err == nil {
					b = own
				}
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(e.out, "Wrote %s. Edit it, then `barnraise documents preview %s` to see it and commit it so every machine uses it.\n", relPath(root, dst), n)
			return nil
		}}
	c.Flags().StringVar(&area, "area", "", "only for items of this area")
	c.Flags().StringVar(&kind, "kind", "", "only for items of this kind (feature, bug, improvement…)")
	c.Flags().BoolVar(&force, "force", false, "overwrite the project's file")
	return c
}

func (e *env) documentsPreviewCmd() *cobra.Command {
	var area, kind string
	c := &cobra.Command{Use: "preview <name>", Short: "Render a document with a sample item", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			n, err := docName(args[0])
			if err != nil {
				return err
			}
			root, err := findRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			d := app.SampleDocData()
			if area != "" {
				d.Item.Area = area
			}
			if kind != "" {
				d.Item.Kind = kind
			}
			if n == domain.DocIssue {
				d.Default = "## Problem\n\nThe built-in issue body goes here.\n"
			}
			out, err := app.RenderDocument(cfg, n, d)
			fmt.Fprint(e.out, out)
			if err != nil {
				return err
			}
			return nil
		}}
	c.Flags().StringVar(&area, "area", "", "the sample item's area")
	c.Flags().StringVar(&kind, "kind", "", "the sample item's kind")
	return c
}

func (e *env) documentsCheckCmd() *cobra.Command {
	return &cobra.Command{Use: "check", Short: "Render every project document with a sample item and report the ones that fail", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := findRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			res := app.CheckDocuments(cfg)
			if len(res) == 0 {
				fmt.Fprintln(e.out, "The project uses the built-in documents.")
				return nil
			}
			files := make([]string, 0, len(res))
			for f := range res {
				files = append(files, f)
			}
			sort.Strings(files)
			bad := 0
			for _, f := range files {
				if err := res[f]; err != nil {
					bad++
					fmt.Fprintf(e.out, "✘ %s: %v\n", f, err)
				} else {
					fmt.Fprintf(e.out, "✔ %s\n", f)
				}
			}
			if bad > 0 {
				fmt.Fprintln(e.out, "A template that fails is replaced by the built-in one when Barnraise writes the document.")
				return exitCode(1)
			}
			return nil
		}}
}

func relPath(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}
