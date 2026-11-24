package knowledge

import (
	"slices"
	"strings"
	"testing"
)

type wantSym struct {
	name, kind string
	line, end  int
}

func checkSyms(t *testing.T, file string, got []codeSymbol, want []wantSym) {
	t.Helper()
	var g []wantSym
	for _, s := range got {
		g = append(g, wantSym{s.Name, s.Kind, s.Line, s.EndLine})
	}
	if !slices.Equal(g, want) {
		t.Errorf("%s:\n got  %v\n want %v", file, g, want)
	}
}

func symByName(syms []codeSymbol, name string) *codeSymbol {
	for i := range syms {
		if syms[i].Name == name {
			return &syms[i]
		}
	}
	return nil
}

// The old line-based extractor found declarations inside comments and
// strings, ended every body at the next declaration and qualified methods
// by indentation. These fixtures are what it got wrong.
func TestNativeTypeScript(t *testing.T) {
	src := `/* function ghost() {
}
*/
const banner = "function fake() {";
const tpl = ` + "`class Nope { ${banner} }`" + `;

export class Store {
  private items = new Map<string, number>();

  load(key: string): number {
    const s = "}"; // a brace in a string
    const re = /[{]/;
    function local() { return 1 }
    return this.items.get(key) ?? fetchCount(key);
  }

  save(): void {
    persist(this.items);
  }
}

export const LIMIT = 10;
export const double = (x: number) => x * 2;
`
	syms := extractNative("typescript", []byte(src))
	checkSyms(t, "store.ts", syms, []wantSym{
		{"Store", "class", 7, 20},
		{"Store.load", "method", 10, 15},
		{"Store.save", "method", 17, 19},
		{"LIMIT", "const", 22, 22},
		{"double", "func", 23, 23},
	})
	load := symByName(syms, "Store.load")
	if !slices.Equal(load.Calls, []string{"fetchCount", "get", "local"}) {
		t.Errorf("calls of load: %v", load.Calls)
	}
	if load.Signature != "load(key: string): number" {
		t.Errorf("signature: %q", load.Signature)
	}
}

func TestNativePython(t *testing.T) {
	src := `"""Module docs.

def fake():
    pass
"""
MAX_RETRIES = 3


class Client:
    """A client.

    def not_a_method(self):
    """

    def get(self, url,
            timeout=MAX_RETRIES):
        def helper():
            return 1
        text = "def nope():"
        return fetch(url)

    # a comment between methods
    async def close(self):
        return None


def main():
    Client().get("x")
`
	syms := extractNative("python", []byte(src))
	checkSyms(t, "client.py", syms, []wantSym{
		{"MAX_RETRIES", "const", 6, 6},
		{"Client", "class", 9, 24},
		{"Client.get", "method", 15, 20},
		{"Client.close", "method", 23, 24},
		{"main", "func", 27, 28},
	})
	get := symByName(syms, "Client.get")
	if get.Signature != "def get(self, url, timeout=MAX_RETRIES)" {
		t.Errorf("signature: %q", get.Signature)
	}
	if !slices.Equal(get.Calls, []string{"fetch", "helper"}) {
		t.Errorf("calls: %v", get.Calls)
	}
}

func TestNativeRust(t *testing.T) {
	src := `pub const MAX: usize = 8;

pub struct Ledger<'a> {
    name: &'a str,
}

impl<'a> Ledger<'a> {
    pub fn open(name: &'a str) -> Self {
        let c = '{';
        let s = r#"fn hidden() {"#;
        Ledger { name }
    }

    fn close(&self) {
        audit(self.name);
    }
}

// fn commented() {}
fn audit(_n: &str) {}
`
	syms := extractNative("rust", []byte(src))
	checkSyms(t, "lib.rs", syms, []wantSym{
		{"MAX", "const", 1, 1},
		{"Ledger", "struct", 3, 5},
		{"Ledger.open", "method", 8, 12},
		{"Ledger.close", "method", 14, 16},
		{"audit", "func", 20, 20},
	})
}

func TestNativeJavaAndKotlin(t *testing.T) {
	java := `package bank;

@Entity
public class Bank {
    public static final int LIMIT = 5;
    private String msg = "public void fake() {";

    @Override
    public void deposit(int x) {
        if (x > LIMIT) {
            log("}" + x);
        }
    }

    public Bank() {
    }
}
`
	syms := extractNative("java", []byte(java))
	checkSyms(t, "Bank.java", syms, []wantSym{
		{"Bank", "class", 4, 17},
		{"Bank.LIMIT", "const", 5, 5},
		{"Bank.deposit", "method", 9, 13},
		{"Bank.Bank", "method", 15, 16},
	})
	if b := symByName(syms, "Bank"); !strings.HasPrefix(b.Text, "@Entity\n") {
		t.Errorf("the annotation belongs to the region: %q", b.Text[:20])
	}
	kotlin := `class Screen(private val repo: Repo) {
    fun title(): String = "Screen ${repo.name} {"

    fun render() {
        draw("}")
    }
}

const val VERSION = "1"
fun top() = Screen(Repo()).render()
`
	syms = extractNative("kotlin", []byte(kotlin))
	checkSyms(t, "Screen.kt", syms, []wantSym{
		{"Screen", "class", 1, 7},
		{"Screen.title", "method", 2, 2},
		{"Screen.render", "method", 4, 6},
		{"VERSION", "const", 9, 9},
		{"top", "func", 10, 10},
	})
}

func TestNativeGoFallback(t *testing.T) {
	// Does not parse (missing brace at the end), so go/ast gives up.
	src := "package x\n\nvar s = `func Fake() {`\n\nfunc (s *Srv) Run() {\n\tif true {\n\t\tgo work()\n\t}\n}\n\nfunc broken( {\n"
	syms := extractSymbols("x.go", []byte(src))
	if len(syms) == 0 || syms[0].Name != "Srv.Run" || syms[0].EndLine != 9 {
		t.Fatalf("go fallback: %+v", syms)
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct{ lang, in, want string }{
		{"typescript", `a = "x{y" // c {`, `a = "   "       `},
		{"typescript", "t = `a${ {b: 1} }c`", "t = `    {b: 1}   `"},
		{"typescript", `r = /a}b/g; x = 4 / 2 / 1`, `r = /   /g; x = 4 / 2 / 1`},
		{"python", "s = '''a\n{'''\n# c", "s = ''' \n '''\n   "},
		{"rust", `let c = '}'; fn f<'a>(x: &'a str) {}`, `let c = ' '; fn f<'a>(x: &'a str) {}`},
		{"rust", `let s = r##"a"#b"##;`, `let s = r##"    "##;`},
		{"kotlin", "/* a /* b */ c */ d", "                  d"},
		{"go", "s := `a\n}`", "s := ` \n `"},
		{"java", "s = \"\"\"\n{\n\"\"\";", "s = \"\"\"\n \n\"\"\";"},
	}
	for _, c := range cases {
		if got := string(sanitize([]byte(c.in), lexOf(c.lang))); got != c.want {
			t.Errorf("%s %q:\n got  %q\n want %q", c.lang, c.in, got, c.want)
		}
	}
}
