package knowledge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

// Symbol extraction modes of a project (knowledge.symbols in barnraise.yaml).
const (
	SymbolsAuto   = "auto"   // ast-grep when it is installed, the native extractor otherwise
	SymbolsNative = "native" // never run ast-grep
)

// Extractor names recorded in the meta of file documents.
const (
	extractorGo      = "go/ast"
	extractorAstGrep = "ast-grep"
	extractorNative  = "native"
)

// astGrep runs the ast-grep CLI (https://ast-grep.github.io) to find
// declarations with a real syntax tree. It is optional: when the binary is
// missing, or fails for a language, the native extractor does the work.
// Only the kinds and ranges of declarations come from ast-grep; names,
// signatures, regions and calls are read from the source Barnraise already
// has, and a file whose matches do not agree with that source falls back
// to the native extractor.
type astGrep struct {
	exec       ports.Exec
	candidates []string

	mu       sync.Mutex
	resolved bool
	bin      string
	broken   map[string]bool // ast-grep language -> failed once, not tried again
}

func newAstGrep(ex ports.Exec, candidates ...string) *astGrep {
	if len(candidates) == 0 {
		// "sg" is also the name of a shadow-utils command; --version tells them apart.
		candidates = []string{"ast-grep", "sg"}
	}
	return &astGrep{exec: ex, candidates: candidates, broken: map[string]bool{}}
}

// binary returns the ast-grep command, or "" when it is not installed.
// It is looked up once per process.
func (a *astGrep) binary(ctx context.Context) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.resolved {
		return a.bin
	}
	a.resolved = true
	for _, c := range a.candidates {
		res, err := a.exec.Run(ctx, ports.Cmd{Name: c, Args: []string{"--version"}, Timeout: 10 * time.Second})
		if err != nil || res.ExitCode != 0 {
			continue
		}
		if strings.Contains(strings.ToLower(string(res.Stdout)+string(res.Stderr)), "ast-grep") {
			a.bin = c
			break
		}
	}
	return a.bin
}

func (a *astGrep) isBroken(lang string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.broken[lang]
}

func (a *astGrep) markBroken(lang string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.broken[lang] = true
}

// agLangOf maps a file to the ast-grep language name its rules use.
func agLangOf(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".ts", ".mts", ".cts":
		return "TypeScript"
	case ".tsx":
		return "Tsx"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "JavaScript"
	case ".py":
		return "Python"
	case ".rs":
		return "Rust"
	case ".java":
		return "Java"
	case ".kt", ".kts":
		return "Kotlin"
	case ".swift":
		return "Swift"
	}
	return ""
}

// How the name of a declaration is read from its header.
const (
	nameClass  = iota // after class, struct, interface, enum... (the keyword is the kind)
	nameFunc          // after function, fn, def, fun, func
	nameMethod        // the identifier before the first parenthesis
	nameVar           // after const, let, var, static, val, or the first identifier
	nameImpl          // the type of a Rust impl block
)

// agRule is one built-in ast-grep rule: a syntax kind and what it means.
type agRule struct {
	kind      string // tree-sitter kind matched by the rule
	extra     string // more YAML lines under rule:, indented by two spaces
	name      int
	symKind   string // fixed kind; empty: the keyword found by nameClass
	container bool
	symbol    bool
}

var (
	tsRules = []agRule{
		{kind: "function_declaration", name: nameFunc, symKind: "func", symbol: true},
		{kind: "generator_function_declaration", name: nameFunc, symKind: "func", symbol: true},
		{kind: "class_declaration", name: nameClass, container: true, symbol: true},
		{kind: "abstract_class_declaration", name: nameClass, container: true, symbol: true},
		{kind: "interface_declaration", name: nameClass, container: true, symbol: true},
		{kind: "type_alias_declaration", name: nameClass, symbol: true},
		{kind: "enum_declaration", name: nameClass, container: true, symbol: true},
		{kind: "method_definition", name: nameMethod, symKind: "func", symbol: true},
		{kind: "variable_declarator", extra: "  has:\n    kind: arrow_function\n", name: nameVar, symKind: "func", symbol: true},
		{kind: "lexical_declaration", extra: "  inside:\n    kind: export_statement\n", name: nameVar, symKind: "const", symbol: true},
	}
	jsRules = []agRule{
		{kind: "function_declaration", name: nameFunc, symKind: "func", symbol: true},
		{kind: "generator_function_declaration", name: nameFunc, symKind: "func", symbol: true},
		{kind: "class_declaration", name: nameClass, container: true, symbol: true},
		{kind: "method_definition", name: nameMethod, symKind: "func", symbol: true},
		{kind: "variable_declarator", extra: "  has:\n    kind: arrow_function\n", name: nameVar, symKind: "func", symbol: true},
		{kind: "lexical_declaration", extra: "  inside:\n    kind: export_statement\n", name: nameVar, symKind: "const", symbol: true},
	}
	agRules = map[string][]agRule{
		"TypeScript": tsRules,
		"Tsx":        tsRules,
		"JavaScript": jsRules,
		"Python": {
			{kind: "function_definition", name: nameFunc, symKind: "func", symbol: true},
			{kind: "class_definition", name: nameClass, symKind: "class", container: true, symbol: true},
			{kind: "expression_statement", extra: "  regex: '^[A-Z][A-Z0-9_]*\\s*[:=]'\n  inside:\n    kind: module\n", name: nameVar, symKind: "const", symbol: true},
		},
		"Rust": {
			{kind: "function_item", name: nameFunc, symKind: "func", symbol: true},
			{kind: "function_signature_item", name: nameFunc, symKind: "func", symbol: true},
			{kind: "struct_item", name: nameClass, container: true, symbol: true},
			{kind: "enum_item", name: nameClass, container: true, symbol: true},
			{kind: "union_item", name: nameClass, container: true, symbol: true},
			{kind: "trait_item", name: nameClass, container: true, symbol: true},
			{kind: "type_item", name: nameClass, symbol: true},
			{kind: "const_item", name: nameVar, symKind: "const", symbol: true},
			{kind: "static_item", name: nameVar, symKind: "const", symbol: true},
			{kind: "impl_item", name: nameImpl, symKind: "impl", container: true},
		},
		"Java": {
			{kind: "class_declaration", name: nameClass, container: true, symbol: true},
			{kind: "interface_declaration", name: nameClass, container: true, symbol: true},
			{kind: "enum_declaration", name: nameClass, container: true, symbol: true},
			{kind: "record_declaration", name: nameClass, container: true, symbol: true},
			{kind: "method_declaration", name: nameMethod, symKind: "func", symbol: true},
			{kind: "constructor_declaration", name: nameMethod, symKind: "func", symbol: true},
		},
		"Kotlin": {
			{kind: "class_declaration", name: nameClass, container: true, symbol: true},
			{kind: "object_declaration", name: nameClass, container: true, symbol: true},
			{kind: "function_declaration", name: nameFunc, symKind: "func", symbol: true},
		},
		"Swift": {
			{kind: "class_declaration", name: nameClass, container: true, symbol: true},
			{kind: "protocol_declaration", name: nameClass, container: true, symbol: true},
			{kind: "function_declaration", name: nameFunc, symKind: "func", symbol: true},
			{kind: "protocol_function_declaration", name: nameFunc, symKind: "func", symbol: true},
		},
	}
)

// inlineRules renders the rules of a language for --inline-rules.
func inlineRules(lang string) string {
	var b strings.Builder
	for i, r := range agRules[lang] {
		if i > 0 {
			b.WriteString("---\n")
		}
		fmt.Fprintf(&b, "id: barnraise-%d\nlanguage: %s\nseverity: hint\nrule:\n  kind: %s\n%s", i, lang, r.kind, r.extra)
	}
	return b.String()
}

// agMatch is the part of ast-grep's JSON output Barnraise reads.
type agMatch struct {
	Text  string `json:"text"`
	File  string `json:"file"`
	Rule  string `json:"ruleId"`
	Range struct {
		Bytes struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"byteOffset"`
		Start struct {
			Line int `json:"line"`
		} `json:"start"`
		End struct {
			Line int `json:"line"`
		} `json:"end"`
	} `json:"range"`
}

const (
	agChunk     = 50      // files per ast-grep run
	agMaxOutput = 8 << 20 // more output than this may have been cut by the runner
	agTimeout   = 2 * time.Minute
)

// symbols extracts the declarations of the given files with ast-grep. The
// result has an entry for every file ast-grep handled (possibly with no
// symbols); the others are left to the native extractor.
func (a *astGrep) symbols(ctx context.Context, root string, work []fileWork) map[string][]codeSymbol {
	bin := a.binary(ctx)
	if bin == "" {
		return nil
	}
	byLang := map[string][]fileWork{}
	for _, w := range work {
		if strings.HasPrefix(w.rel, docsDir+"/") {
			continue
		}
		if l := agLangOf(w.rel); l != "" && !a.isBroken(l) {
			byLang[l] = append(byLang[l], w)
		}
	}
	langs := make([]string, 0, len(byLang))
	for l := range byLang {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	out := map[string][]codeSymbol{}
	for _, l := range langs {
		files := byLang[l]
		for len(files) > 0 && !a.isBroken(l) {
			n := min(agChunk, len(files))
			for rel, syms := range a.run(ctx, bin, root, l, files[:n]) {
				out[rel] = syms
			}
			files = files[n:]
		}
	}
	return out
}

func (a *astGrep) run(ctx context.Context, bin, root, lang string, files []fileWork) map[string][]codeSymbol {
	args := []string{"scan", "--inline-rules", inlineRules(lang), "--json=stream"}
	src := map[string][]byte{}
	for _, w := range files {
		args = append(args, w.rel)
		src[w.rel] = w.src
	}
	res, err := a.exec.Run(ctx, ports.Cmd{Name: bin, Args: args, Dir: root, Timeout: agTimeout})
	if err != nil {
		return nil // a timeout or a cancel: the native extractor does this batch
	}
	if len(res.Stdout) >= agMaxOutput {
		return nil
	}
	matches := map[string][]agMatch{}
	parsed := 0
	sc := bufio.NewScanner(bytes.NewReader(res.Stdout))
	sc.Buffer(make([]byte, 0, 64<<10), agMaxOutput)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m agMatch
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		parsed++
		rel := strings.TrimPrefix(path.Clean(strings.ReplaceAll(m.File, "\\", "/")), "./")
		if _, ok := src[rel]; ok {
			matches[rel] = append(matches[rel], m)
		}
	}
	if res.ExitCode != 0 && parsed == 0 {
		// Rules this version of ast-grep does not accept: stop asking it
		// about this language for the life of the process.
		a.markBroken(lang)
		return nil
	}
	out := map[string][]codeSymbol{}
	for _, w := range files {
		if syms, ok := agSymbols(langOf(w.rel), lang, w.src, matches[w.rel]); ok {
			out[w.rel] = syms
		}
	}
	return out
}

var (
	reClassName  = regexp.MustCompile(`\b(class|interface|enum|struct|trait|union|protocol|object|record|actor|extension|type|typealias|mixin)\s+(?:(?:class|interface)\s+)?([A-Za-z_$][\w$]*)`)
	reFuncName   = regexp.MustCompile(`\b(?:function|fn|def|fun|func)\b\s*\*?\s*(?:<[^>]*>\s*)?(?:[\w.<>?]+\.)?([A-Za-z_$][\w$]*)`)
	reMethodName = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*(?:<[^<>()]*>)?\s*\(`)
	reVarName    = regexp.MustCompile(`\b(?:const|let|var|static|val)\s+(?:mut\s+)?([A-Za-z_$][\w$]*)`)
	reFirstIdent = regexp.MustCompile(`^\s*([A-Za-z_$][\w$]*)`)
	reAnnotation = regexp.MustCompile(`@[A-Za-z_][\w.]*(?:\([^()]*\))?|#!?\[[^\]]*\]`)
	reImplName   = regexp.MustCompile(`\bimpl(?:\s*<[^>]*>)?\s+(?:[\w:<>, ]+\s+for\s+)?(?:\w+::)*([A-Za-z_]\w*)`)
)

var modifierWords = kw("public private protected internal static final abstract async override open sealed data inline " +
	"suspend synchronized native default export declare readonly get set pub unsafe extern mut")

// declName reads the name (and, for class-like declarations, the keyword)
// from the sanitized header of a declaration.
func declName(how int, header string) (name, keyword string) {
	switch how {
	case nameClass:
		if m := reClassName.FindStringSubmatch(header); m != nil {
			return m[2], m[1]
		}
	case nameFunc:
		if m := reFuncName.FindStringSubmatch(header); m != nil {
			return m[1], ""
		}
	case nameMethod:
		for _, m := range reMethodName.FindAllStringSubmatch(header, -1) {
			if !modifierWords[m[1]] && !typeKeyword[m[1]] {
				return m[1], ""
			}
		}
	case nameVar:
		if m := reVarName.FindStringSubmatch(header); m != nil {
			return m[1], ""
		}
		if m := reFirstIdent.FindStringSubmatch(header); m != nil {
			return m[1], ""
		}
	case nameImpl:
		if m := reImplName.FindStringSubmatch(header); m != nil {
			return m[1], ""
		}
	}
	return "", ""
}

// agSymbols turns the matches of one file into symbols. ok is false when a
// match does not agree with the source (the file changed in between, or
// offsets are not what this version means): the caller falls back.
func agSymbols(lang, agLang string, src []byte, ms []agMatch) ([]codeSymbol, bool) {
	spec := langSpecs[lang]
	if spec == nil {
		return nil, false
	}
	rules := agRules[agLang]
	lex := lexOf(lang)
	if bytes.Contains(src, []byte("\r\n")) {
		return nil, false // offsets would not match the normalized lines
	}
	clean := sanitize(src, lex)
	orig := strings.Split(string(src), "\n")
	cl := strings.Split(string(clean), "\n")
	var decls []rawDecl
	for _, m := range ms {
		var idx int
		if _, err := fmt.Sscanf(m.Rule, "barnraise-%d", &idx); err != nil || idx < 0 || idx >= len(rules) {
			continue
		}
		r := rules[idx]
		s, e := m.Range.Bytes.Start, m.Range.Bytes.End
		if s < 0 || e > len(src) || s > e || string(src[s:e]) != m.Text {
			return nil, false
		}
		line, end := m.Range.Start.Line, m.Range.End.Line
		if line < 0 || end >= len(cl) || line > end {
			return nil, false
		}
		// The declaration line comes after its annotations and attributes.
		hb := []byte(string(clean[s:min(e, s+600)]))
		hb = reAnnotation.ReplaceAllFunc(hb, func(x []byte) []byte {
			return bytes.Map(func(r rune) rune {
				if r == '\n' {
					return r
				}
				return ' '
			}, x)
		})
		off := len(hb) - len(bytes.TrimLeft(hb, " \t\r\n"))
		line += bytes.Count(hb[:off], []byte("\n"))
		header := string(hb[off:])
		line = min(line, end)
		name, keyword := declName(r.name, header)
		if name == "" || spec.keywords[name] {
			continue
		}
		kind := r.symKind
		symbol := r.symbol
		if kind == "" {
			kind = keyword
			if kind == "typealias" {
				kind = "type"
			}
		}
		if keyword == "extension" {
			kind, symbol = "extension", false
		}
		decls = append(decls, rawDecl{name: name, kind: kind, line: line, end: end, container: r.container, symbol: symbol})
	}
	sort.SliceStable(decls, func(i, j int) bool {
		if decls[i].line != decls[j].line {
			return decls[i].line < decls[j].line
		}
		return decls[i].end > decls[j].end
	})
	// export const f = () => ... matches as a function and as a constant: keep the function.
	funcs := map[string]bool{}
	for _, d := range decls {
		if d.kind == "func" {
			funcs[fmt.Sprintf("%s@%d", d.name, d.line)] = true
		}
	}
	kept := decls[:0]
	for _, d := range decls {
		if d.kind == "const" && funcs[fmt.Sprintf("%s@%d", d.name, d.line)] {
			continue
		}
		kept = append(kept, d)
	}
	return finishDecls(spec, lex, orig, cl, kept), true
}
