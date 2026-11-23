package knowledge

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strings"
)

// codeSymbol is one extracted declaration with its body region.
type codeSymbol struct {
	Name      string // qualified: Type.Method for methods
	Kind      string // func, method, struct, interface, type, class, object, enum, trait, protocol...
	Line      int
	EndLine   int
	Signature string
	Text      string   // the source region (doc comment + declaration + body)
	Calls     []string // short names called inside the body
}

func shortName(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// langOf maps a file extension to a language name; "" means not code.
func langOf(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".go":
		return "go"
	case ".kt", ".kts":
		return "kotlin"
	case ".java":
		return "java"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".swift":
		return "swift"
	case ".dart":
		return "dart"
	}
	return ""
}

const maxChunkBytes = 16 << 10

// extractSymbols returns the symbols of a source file.
func extractSymbols(file string, src []byte) []codeSymbol {
	lang := langOf(file)
	if lang == "go" {
		if syms, ok := extractGo(src); ok {
			return syms
		}
	}
	spec := langSpecs[lang]
	if spec == nil {
		return nil
	}
	return extractRegex(spec, src)
}

// extractGo uses go/parser: funcs, methods as Recv.Name, and types.
func extractGo(src []byte) ([]codeSymbol, bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	region := func(from, to token.Pos) string {
		a, b := fset.Position(from).Offset, fset.Position(to).Offset
		if a < 0 || b > len(src) || a > b {
			return ""
		}
		return clip(string(src[a:b]))
	}
	var out []codeSymbol
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name, kind := d.Name.Name, "func"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if r := recvName(d.Recv.List[0].Type); r != "" {
					name, kind = r+"."+name, "method"
				}
			}
			sig := *d
			sig.Body, sig.Doc = nil, nil
			var buf bytes.Buffer
			printer.Fprint(&buf, token.NewFileSet(), &sig)
			start := d.Pos()
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
			s := codeSymbol{
				Name: name, Kind: kind, Line: fset.Position(d.Pos()).Line, EndLine: fset.Position(d.End()).Line,
				Signature: oneLine(buf.String(), 300), Text: region(start, d.End()),
			}
			if d.Body != nil {
				s.Calls = goCalls(d.Body, d.Name.Name)
			}
			out = append(out, s)
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, sp := range d.Specs {
				ts := sp.(*ast.TypeSpec)
				kind, sig := "type", ""
				switch ts.Type.(type) {
				case *ast.StructType:
					kind, sig = "struct", "type "+ts.Name.Name+" struct"
				case *ast.InterfaceType:
					kind, sig = "interface", "type "+ts.Name.Name+" interface"
				default:
					var buf bytes.Buffer
					printer.Fprint(&buf, token.NewFileSet(), ts)
					sig = "type " + buf.String()
				}
				start, end := ts.Pos(), ts.End()
				if len(d.Specs) == 1 {
					start, end = d.Pos(), d.End()
					if d.Doc != nil {
						start = d.Doc.Pos()
					}
				} else if ts.Doc != nil {
					start = ts.Doc.Pos()
				}
				s := codeSymbol{
					Name: ts.Name.Name, Kind: kind, Line: fset.Position(ts.Pos()).Line, EndLine: fset.Position(end).Line,
					Signature: oneLine(sig, 300), Text: region(start, end),
				}
				out = append(out, s)
			}
		}
	}
	return out, true
}

func recvName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

func goCalls(body ast.Node, self string) []string {
	seen := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := c.Fun.(type) {
		case *ast.Ident:
			seen[f.Name] = true
		case *ast.SelectorExpr:
			seen[f.Sel.Name] = true
		case *ast.IndexExpr: // generic call f[T](x)
			if id, ok := f.X.(*ast.Ident); ok {
				seen[id.Name] = true
			}
		}
		return true
	})
	delete(seen, self)
	for b := range goBuiltins {
		delete(seen, b)
	}
	return sortedKeys(seen)
}

var goBuiltins = map[string]bool{"append": true, "len": true, "cap": true, "make": true, "new": true, "panic": true, "recover": true,
	"copy": true, "delete": true, "close": true, "print": true, "println": true, "min": true, "max": true, "clear": true,
	"string": true, "int": true, "int64": true, "float64": true, "byte": true, "rune": true, "error": true}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func clip(s string) string {
	if len(s) <= maxChunkBytes {
		return s
	}
	cut := maxChunkBytes
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

// declPattern recognizes one kind of declaration on a single line.
type declPattern struct {
	re        *regexp.Regexp
	kind      string // fixed kind; empty means group "kind" of the regexp
	container bool   // methods declared inside get qualified by its name
	symbol    bool   // recorded as a symbol (false: container only, e.g. impl)
	inside    bool   // only valid inside a container (bare method syntax)
}

type langSpec struct {
	decls    []declPattern
	comment  []string // line comment prefixes
	keywords map[string]bool
}

func kw(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

var commonKeywords = "if else for while switch case catch return new throw try do when match yield await typeof sizeof " +
	"function fun fn def func class super this self Self init constructor elif in is not and or print assert with lambda"

var langSpecs = map[string]*langSpec{
	"kotlin": {
		decls: []declPattern{
			{re: regexp.MustCompile(`^\s*(?:(?:public|private|internal|protected|abstract|open|final|sealed|data|enum|annotation|inner|value|inline|expect|actual|external|fun)\s+)*(?P<kind>class|interface|object)\s+(?P<name>[A-Za-z_]\w*)`), container: true, symbol: true},
			{re: regexp.MustCompile(`^\s*(?:(?:public|private|internal|protected|abstract|open|final|override|suspend|inline|infix|operator|tailrec|external|expect|actual)\s+)*fun\s+(?:<[^>]+>\s*)?(?:[\w.<>?, ]+\.)?(?P<name>[A-Za-z_]\w*)\s*\(`), kind: "func", symbol: true},
		},
		comment: []string{"//", "*", "/*"},
	},
	"java": {
		decls: []declPattern{
			{re: regexp.MustCompile(`^\s*(?:@\w+\s+)*(?:(?:public|private|protected|static|final|abstract|sealed|non-sealed|strictfp)\s+)*(?P<kind>class|interface|enum|record)\s+(?P<name>[A-Za-z_]\w*)`), container: true, symbol: true},
			{re: regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract|synchronized|native|default|strictfp)\s+)*(?:<[^>]+>\s+)?(?P<type>[\w.<>\[\]?,]+(?:\s*<[^>]*>)?)\s+(?P<name>[A-Za-z_]\w*)\s*\([^=]*$`), kind: "func", symbol: true},
			{re: regexp.MustCompile(`^\s*(?:public|private|protected)\s+(?P<name>[A-Z]\w*)\s*\([^=;]*$`), kind: "func", symbol: true, inside: true}, // constructors
		},
		comment: []string{"//", "*", "/*"},
	},
	"typescript": tsSpec(),
	"javascript": tsSpec(),
	"python": {
		decls: []declPattern{
			{re: regexp.MustCompile(`^\s*class\s+(?P<name>[A-Za-z_]\w*)`), kind: "class", container: true, symbol: true},
			{re: regexp.MustCompile(`^\s*(?:async\s+)?def\s+(?P<name>[A-Za-z_]\w*)\s*\(`), kind: "func", symbol: true},
		},
		comment: []string{"#"},
	},
	"rust": {
		decls: []declPattern{
			{re: regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?P<kind>struct|enum|trait|union)\s+(?P<name>[A-Za-z_]\w*)`), container: true, symbol: true},
			{re: regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?type\s+(?P<name>[A-Za-z_]\w*)`), kind: "type", symbol: true},
			{re: regexp.MustCompile(`^\s*impl(?:\s*<[^>]*>)?\s+(?:[\w:<>, ]+\s+for\s+)?(?:\w+::)*(?P<name>[A-Za-z_]\w*)`), kind: "impl", container: true},
			{re: regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:(?:const|async|unsafe|extern(?:\s+"[^"]*")?)\s+)*fn\s+(?P<name>[A-Za-z_]\w*)`), kind: "func", symbol: true},
		},
		comment: []string{"//", "*", "/*"},
	},
	"swift": {
		decls: []declPattern{
			{re: regexp.MustCompile(`^\s*(?:@\w+\s+)*(?:(?:public|private|internal|fileprivate|open|final|indirect)\s+)*(?P<kind>class|struct|enum|protocol|actor)\s+(?P<name>[A-Za-z_]\w*)`), container: true, symbol: true},
			{re: regexp.MustCompile(`^\s*(?:(?:public|private|internal|fileprivate)\s+)*extension\s+(?P<name>[A-Za-z_]\w*)`), kind: "extension", container: true},
			{re: regexp.MustCompile(`^\s*(?:@\w+\s+)*(?:(?:public|private|internal|fileprivate|open|final|static|class|override|mutating|nonmutating|convenience|required)\s+)*func\s+(?P<name>[A-Za-z_]\w*)`), kind: "func", symbol: true},
		},
		comment: []string{"//", "*", "/*"},
	},
	"dart": {
		decls: []declPattern{
			{re: regexp.MustCompile(`^\s*(?:(?:abstract|sealed|base|final|interface)\s+)*(?P<kind>class|mixin|enum)\s+(?P<name>[A-Za-z_]\w*)`), container: true, symbol: true},
			{re: regexp.MustCompile(`^\s*extension\s+(?P<name>[A-Za-z_]\w*)`), kind: "extension", container: true},
			{re: regexp.MustCompile(`^\s*(?:(?:static|external|abstract|factory|@override)\s+)*(?P<type>[\w<>?,\[\]]+(?:<[^>]*>)?\??)\s+(?P<name>[A-Za-z_]\w*)\s*(?:<[^>]*>)?\s*\([^;]*\)\s*(?:async\*?|sync\*)?\s*(?:\{|=>)`), kind: "func", symbol: true},
			{re: regexp.MustCompile(`^(?P<name>[A-Za-z_]\w*)\s*\([^;]*\)\s*(?:async\*?)?\s*\{`), kind: "func", symbol: true}, // top-level main() {
		},
		comment: []string{"//", "*", "/*"},
	},
	"go": { // fallback when the file does not parse
		decls: []declPattern{
			{re: regexp.MustCompile(`^type\s+(?P<name>[A-Za-z_]\w*)`), kind: "type", symbol: true},
			{re: regexp.MustCompile(`^func\s+(?:\(\s*\w*\s*\*?(?P<recv>\w+)[^)]*\)\s*)?(?P<name>[A-Za-z_]\w*)`), kind: "func", symbol: true},
		},
		comment: []string{"//"},
	},
}

func tsSpec() *langSpec {
	return &langSpec{
		decls: []declPattern{
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?(?P<kind>class|interface|enum)\s+(?P<name>[A-Za-z_$][\w$]*)`), container: true, symbol: true},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?type\s+(?P<name>[A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*=`), kind: "type", symbol: true},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:async\s+)?function\s*\*?\s*(?P<name>[A-Za-z_$][\w$]*)`), kind: "func", symbol: true},
			{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+(?P<name>[A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*(?::[^=]+)?=>|[A-Za-z_$][\w$]*\s*=>)`), kind: "func", symbol: true},
			{re: regexp.MustCompile(`^\s+(?:(?:public|private|protected|static|readonly|abstract|override|async|get|set)\s+)*\*?(?P<name>[A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*\([^)]*\)\s*(?::\s*[^{;=]+)?\{\s*$`), kind: "func", symbol: true, inside: true},
		},
		comment: []string{"//", "*", "/*"},
	}
}

func init() {
	for _, s := range langSpecs {
		s.keywords = kw(commonKeywords)
	}
}

var (
	reCall      = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*\(`)
	typeKeyword = kw("return new throw else case yield await print echo goto")
)

type container struct {
	name   string
	indent int
}

func indentOf(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// extractRegex is a line-based best-effort extractor: one declaration per
// line, the body region runs until the next declaration; methods are
// qualified by the enclosing container found by indentation.
func extractRegex(spec *langSpec, src []byte) []codeSymbol {
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	type hit struct {
		line   int
		name   string
		kind   string
		symbol bool
		sig    string
	}
	var hits []hit
	var stack []container
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || hasAnyPrefix(t, spec.comment) {
			continue
		}
		ind := indentOf(ln)
		for _, p := range spec.decls {
			m := p.re.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			name, kind, recv := "", p.kind, ""
			for gi, g := range p.re.SubexpNames() {
				switch g {
				case "name":
					name = m[gi]
				case "kind":
					kind = m[gi]
				case "recv":
					recv = m[gi]
				}
			}
			if g := p.re.SubexpIndex("type"); g >= 0 && (typeKeyword[m[g]] || spec.keywords[m[g]]) {
				continue
			}
			if name == "" || spec.keywords[name] {
				continue
			}
			for len(stack) > 0 && stack[len(stack)-1].indent >= ind {
				stack = stack[:len(stack)-1]
			}
			if p.inside && len(stack) == 0 {
				continue
			}
			full := name
			if recv != "" {
				full, kind = recv+"."+name, "method"
			} else if !p.container && len(stack) > 0 {
				full, kind = stack[len(stack)-1].name+"."+name, "method"
			}
			if p.container {
				stack = append(stack, container{name: name, indent: ind})
			}
			sig := t
			if j := strings.LastIndex(sig, "{"); j > 0 && strings.TrimSpace(sig[j+1:]) == "" {
				sig = strings.TrimSpace(sig[:j])
			}
			hits = append(hits, hit{line: i, name: full, kind: kind, symbol: p.symbol, sig: oneLine(sig, 300)})
			break
		}
	}
	var out []codeSymbol
	for hi, h := range hits {
		if !h.symbol {
			continue
		}
		end := len(lines)
		if hi+1 < len(hits) {
			end = hits[hi+1].line
		}
		if end-h.line > 400 {
			end = h.line + 400
		}
		// trim trailing blank lines from the region
		for end > h.line+1 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		region := strings.Join(lines[h.line:end], "\n")
		s := codeSymbol{Name: h.name, Kind: h.kind, Line: h.line + 1, EndLine: end, Signature: h.sig, Text: clip(region)}
		if h.kind == "func" || h.kind == "method" {
			body := strings.Join(lines[min(h.line+1, end):end], "\n")
			s.Calls = regexCalls(spec, body, shortName(h.name))
		}
		out = append(out, s)
	}
	return out
}

func regexCalls(spec *langSpec, body, self string) []string {
	seen := map[string]bool{}
	for _, m := range reCall.FindAllStringSubmatch(body, -1) {
		n := m[1]
		if n == self || spec.keywords[n] || typeKeyword[n] || len(n) < 2 {
			continue
		}
		seen[n] = true
	}
	return sortedKeys(seen)
}

func hasAnyPrefix(s string, ps []string) bool {
	for _, p := range ps {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
