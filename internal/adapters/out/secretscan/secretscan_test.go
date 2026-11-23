package secretscan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake secrets are assembled at runtime so this file itself does not trip
// secret scanners (including push protection on the code host).
func j(parts ...string) string { return strings.Join(parts, "") }

var (
	awsKey    = j("AKIA", "Q7ZK3M9TVB2LX4PW")
	ghToken   = j("ghp", "_", "a1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R8")
	ghPAT     = j("github", "_pat_", "11ABCDEFG0123456789_abcdefghijKLMNOP")
	slackTok  = j("xox", "b-", "123456789012-abcdefABCDEF")
	stripeKey = j("sk", "_live_", "4eC39HqLyjWDarjtT1zdp7dc")
	googleKey = j("AI", "za", "SyD3x9Zq1Vb7Lm2Nc8Kp4Rt6Wy0Hj5Gf3Ae")
	jwt       = j("eyJ", "hbGciOiJIUzI1NiJ9", ".", "eyJ", "zdWIiOiIxMjM0NTY3ODkwIn0", ".", "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U")
	pem       = j("-----BEGIN ", "RSA PRIVATE", " KEY-----")
	highEnt   = j("Zx8", "#kQ2", "vLp9", "Wm4t", "Rb7Y")
)

func rules(fs []Finding) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Rule] = true
	}
	return m
}

func TestBuiltInRules(t *testing.T) {
	src := strings.Join([]string{
		"aws_access_key_id = " + awsKey,
		"token: " + ghToken,
		"GH=" + ghPAT,
		"slack = \"" + slackTok + "\"",
		"stripe := \"" + stripeKey + "\"",
		"key=" + googleKey,
		pem,
		"auth = \"Bearer " + jwt + "\"",
		"db_password = \"" + highEnt + "\"",
	}, "\n")
	fs := Scan(map[string][]byte{"config/app.yaml": []byte(src)})
	got := rules(fs)
	for _, r := range []string{"aws-access-key", "github-token", "github-pat", "slack-token", "stripe-live-key", "google-api-key", "private-key", "jwt", "generic-secret"} {
		if !got[r] {
			t.Errorf("rule %s did not fire; findings: %+v", r, fs)
		}
	}
	for _, f := range fs {
		if f.File != "config/app.yaml" || f.Line == 0 {
			t.Errorf("bad position %+v", f)
		}
		for _, s := range []string{awsKey, ghToken, ghPAT, slackTok, stripeKey, googleKey, jwt, highEnt} {
			if strings.Contains(f.Redacted, s) || strings.Contains(f.Redacted, s[4:]) {
				t.Errorf("secret leaked in %+v", f)
			}
		}
	}
	// Exactly one finding per line: specific rules win over generic-secret.
	lines := map[int]int{}
	for _, f := range fs {
		lines[f.Line]++
	}
	for l, n := range lines {
		if n > 1 {
			t.Errorf("line %d has %d findings", l, n)
		}
	}
}

func TestPlaceholdersAndLowEntropy(t *testing.T) {
	src := strings.Join([]string{
		"password = changeme123",
		"api_key = ${API_KEY}",
		"token: xxxxxxxxxxxxxxxx",
		"secret = \"your_secret_here\"",
		"aws_access_key_id = AKIAIOSFODNN7EXAMPLE",
		"password = aaaaaaaaaaaa",
		"token = os.Getenv(\"TOKEN\")",
		"password = hello1234", // low entropy
		"tokenizer = newTokenizer",
	}, "\n")
	if fs := Scan(map[string][]byte{"a.go": []byte(src)}); len(fs) != 0 {
		t.Fatalf("false positives: %+v", fs)
	}
}

func TestKeystoreAndEnvFiles(t *testing.T) {
	fs := Scan(map[string][]byte{
		"app/release.jks":     {0, 1, 2},
		"app/upload.keystore": []byte("x"),
		".env":                []byte("PORT=3000\nDEBUG=true\nDATABASE_URL=postgres://u:" + "pw9" + "x@h/db\nEMPTY=\nAPI=changeme\n"),
		".env.example":        []byte("DATABASE_URL=postgres://u:p@h/db\n"),
		"bin/blob":            {0, 'A', 'K'},
	})
	byFile := map[string][]Finding{}
	for _, f := range fs {
		byFile[f.File] = append(byFile[f.File], f)
	}
	if len(byFile["app/release.jks"]) != 1 || byFile["app/release.jks"][0].Rule != "keystore-file" {
		t.Errorf("jks: %+v", byFile)
	}
	if len(byFile["app/upload.keystore"]) != 1 {
		t.Errorf("keystore: %+v", byFile)
	}
	env := byFile[".env"]
	if len(env) != 1 || env[0].Line != 3 || env[0].Rule != "env-file" || !strings.HasPrefix(env[0].Redacted, "DATABASE_URL=") || strings.Contains(env[0].Redacted, "pw9x") {
		t.Errorf(".env: %+v", env)
	}
	if len(byFile[".env.example"]) != 0 || len(byFile["bin/blob"]) != 0 {
		t.Errorf("templates/binaries: %+v", byFile)
	}
}

func TestScanDiff(t *testing.T) {
	diff := j(`diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -10,3 +10,4 @@ func main() {
 	a := 1
-	old := "`, ghToken, `"
+	tok := "`, ghToken, `"
 	b := 2
+	c := 3
@@ -40,2 +41,3 @@
 	x
+	stripe := "`, stripeKey, `"
diff --git a/keys/release.jks b/keys/release.jks
new file mode 100644
Binary files /dev/null and b/keys/release.jks differ
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-`, awsKey, `
`)
	fs := ScanDiff(diff)
	want := []Finding{
		{File: "keys/release.jks", Rule: "keystore-file"},
		{File: "main.go", Line: 11, Rule: "github-token"},
		{File: "main.go", Line: 42, Rule: "stripe-live-key"},
	}
	if len(fs) != len(want) {
		t.Fatalf("findings %+v", fs)
	}
	for i, w := range want {
		if fs[i].File != w.File || fs[i].Line != w.Line || fs[i].Rule != w.Rule {
			t.Errorf("finding %d = %+v, want %+v", i, fs[i], w)
		}
	}
}

func TestRedactAndEntropy(t *testing.T) {
	if r := Redact("abcdefghijklmnop"); r != "abcd********(16 chars)" {
		t.Fatal(r)
	}
	if Redact("abc") != "***" {
		t.Fatal(Redact("abc"))
	}
	if Entropy("aaaa") != 0 || Entropy("abcd") != 2 {
		t.Fatal("entropy")
	}
}

func TestGitleaksMerge(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "gitleaks")
	// A fake gitleaks: reports one finding on src/extra.txt line 2 and
	// one that duplicates a built-in finding.
	script := `#!/bin/sh
src=""; rep=""
while [ $# -gt 0 ]; do
  case "$1" in
    --source) src="$2"; shift;;
    --report-path) rep="$2"; shift;;
  esac
  shift
done
[ -f "$src/extra.txt" ] || exit 7
more=""
[ -f "$src/main.go" ] && more=',{"RuleID":"github-pat","File":"'"$src"'/main.go","StartLine":1,"Secret":"REDACTED"}'
echo '[{"RuleID":"custom-rule","File":"'"$src"'/extra.txt","StartLine":2,"Secret":"REDACTED"}'"$more]" > "$rep"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New().UseGitleaks(bin)
	if s.Gitleaks() == "" {
		t.Fatal("gitleaks not enabled")
	}
	fs, err := s.Scan(context.Background(), map[string][]byte{
		"main.go":   []byte("t := \"" + ghToken + "\"\n"),
		"extra.txt": []byte("a\nb\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[0].Rule != "gitleaks:custom-rule" || fs[0].File != "extra.txt" || fs[1].Rule != "github-token" {
		t.Fatalf("%+v", fs)
	}
	// Diff mode reconstructs the file.
	diff := "diff --git a/extra.txt b/extra.txt\n--- a/extra.txt\n+++ b/extra.txt\n@@ -1,0 +1,2 @@\n+a\n+b\n"
	fs, err = s.ScanDiff(context.Background(), diff)
	if err != nil || len(fs) != 1 || fs[0].Line != 2 {
		t.Fatalf("%+v %v", fs, err)
	}
	if New().UseGitleaks(filepath.Join(dir, "missing")).Gitleaks() != "" {
		t.Fatal("missing binary must disable gitleaks")
	}
}
