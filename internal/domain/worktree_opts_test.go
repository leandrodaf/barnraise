package domain

import (
	"strconv"
	"testing"
)

func TestItemSlugAndComposeName(t *testing.T) {
	for key, want := range map[string]string{"I-42": "i42", "I-42~b": "i42b", "i-7": "i7"} {
		if got := ItemSlug(key); got != want {
			t.Errorf("ItemSlug(%q) = %q, want %q", key, got, want)
		}
	}
	for _, c := range []struct{ project, key, want string }{
		{"demo", "I-42", "demo-i42"},
		{"My.App", "I-3~b", "my-app-i3b"},
		{"__", "I-1", "barnraise-i1"},
		{"web_api", "I-9", "web_api-i9"},
	} {
		if got := ComposeProjectName(c.project, c.key); got != c.want {
			t.Errorf("ComposeProjectName(%q, %q) = %q, want %q", c.project, c.key, got, c.want)
		}
	}
	if id, att := SplitWorktreeKey("I-4~b"); id != "I-4" || att != "b" {
		t.Fatal(id, att)
	}
}

func TestItemPortRange(t *testing.T) {
	// One port per item is the historic base + number%1000.
	if p, n := ItemPortRange(4100, 1, "I-42"); p != 4142 || n != 1 {
		t.Fatal(p, n)
	}
	if p, _ := ItemPortRange(4100, 0, "I-1042"); p != 4142 {
		t.Fatal(p)
	}
	// Ranges do not overlap between items.
	a, _ := ItemPortRange(4100, 10, "I-1")
	b, _ := ItemPortRange(4100, 10, "I-2")
	if a != 4110 || b != 4120 {
		t.Fatal(a, b)
	}
	// The second attempt shares the item's ports.
	if p, _ := ItemPortRange(4100, 10, "I-2~b"); p != b {
		t.Fatal(p)
	}
	// Never past the last port.
	for n := 1; n < 3000; n += 7 {
		p, size := ItemPortRange(60000, 20, "I-"+strconv.Itoa(n))
		if p+size-1 > 65535 || p < 60000 {
			t.Fatalf("I-%d: %d+%d", n, p, size)
		}
	}
	if p, n := ItemPortRange(4100, 5, "_main-abc"); p != 4100 || n != 5 {
		t.Fatal(p, n)
	}
	if p, _ := ItemPortRange(65530, 100, "I-3"); p != 65530 {
		t.Fatal(p)
	}
}

func TestIsolationEnvRenders(t *testing.T) {
	o := WorktreeOpts{PortRange: 3, Isolate: WorktreeIsolation{Env: map[string]string{
		"DB_NAME":      "app_{{.Item}}",
		"DATABASE_URL": `{{dbURL (env "DATABASE_URL") (print "app_" .Item)}}`,
		"REDIS_URL":    `{{env "REDIS_URL" | replace "/0" "/1"}}`,
		"API_PORT":     "{{index .Ports 1}}",
		"WHERE":        "{{.Project}}/{{.ItemID}}/{{.Compose}}/{{.Dir}}",
	}}}
	v := o.Identity("demo", "I-7", 4100, "/wt/I-7")
	env, err := o.Isolate.Render(v, map[string]string{"DATABASE_URL": "postgres://u:p@db:5432/app?sslmode=disable", "REDIS_URL": "redis://r:6379/0"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"PORT": "4121", "BARNRAISE_PORTS": "4121-4123", "COMPOSE_PROJECT_NAME": "demo-i7", "BARNRAISE_ITEM_SLUG": "i7",
		"DB_NAME": "app_i7", "DATABASE_URL": "postgres://u:p@db:5432/app_i7?sslmode=disable", "REDIS_URL": "redis://r:6379/1",
		"API_PORT": "4122", "WHERE": "demo/I-7/demo-i7//wt/I-7",
	}
	for k, w := range want {
		if env[k] != w {
			t.Errorf("%s = %q, want %q", k, env[k], w)
		}
	}
	if _, err := (WorktreeIsolation{Env: map[string]string{"X": "{{.Nope}}"}}).Render(v, nil); err == nil {
		t.Fatal("an unknown field is an error")
	}
	if _, err := (WorktreeIsolation{Env: map[string]string{"X": "{{"}}).Render(v, nil); err == nil {
		t.Fatal("a broken template is an error")
	}
}

func TestDatabaseURLWithName(t *testing.T) {
	for _, c := range []struct{ in, name, want string }{
		{"postgres://u@h/app", "app_i1", "postgres://u@h/app_i1"},
		{"mysql://u:p@tcp:3306/shop?parseTime=true", "shop_i2", "mysql://u:p@tcp:3306/shop_i2?parseTime=true"},
		{"postgres://u@h", "x", "postgres://u@h/x"},
		{"sqlite:app.db", "app_i3", "sqlite:app_i3.db"},
		{"sqlite:data/app.db", "app_i3", "sqlite:data/app_i3.db"},
		{"", "plain", "plain"},
		{"not a url", "plain", "plain"},
	} {
		if got := DatabaseURLWithName(c.in, c.name); got != c.want {
			t.Errorf("DatabaseURLWithName(%q, %q) = %q, want %q", c.in, c.name, got, c.want)
		}
	}
}

func TestWorktreeOptsValidate(t *testing.T) {
	if err := (WorktreeOpts{}).Validate(); err != nil {
		t.Fatal(err)
	}
	ok := WorktreeOpts{PortRange: 10, Isolate: WorktreeIsolation{Compose: "on", Env: map[string]string{"DB_NAME": "app_{{.Item}}"}}, BuildCache: []string{"target", ".next/cache"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]WorktreeOpts{
		"port range":   {PortRange: 500},
		"compose":      {Isolate: WorktreeIsolation{Compose: "maybe"}},
		"name":         {Isolate: WorktreeIsolation{Env: map[string]string{"1BAD": "x"}}},
		"reserved":     {Isolate: WorktreeIsolation{Env: map[string]string{"BARNRAISE_TOKEN": "x"}}},
		"port":         {Isolate: WorktreeIsolation{Env: map[string]string{"PORT": "1"}}},
		"preload":      {Isolate: WorktreeIsolation{Env: map[string]string{"LD_PRELOAD": "x"}}},
		"proxy":        {Isolate: WorktreeIsolation{Env: map[string]string{"https_proxy": "x"}}},
		"template":     {Isolate: WorktreeIsolation{Env: map[string]string{"X": "{{.Missing}}"}}},
		"twice":        {BuildCache: []string{"target", "target/"}},
		"outside":      {BuildCache: []string{"../x"}},
		"absolute":     {BuildCache: []string{"/etc"}},
		"whole":        {BuildCache: []string{"."}},
		"git":          {BuildCache: []string{".git/objects"}},
		"session":      {BuildCache: []string{".barnraise-session"}},
		"empty":        {BuildCache: []string{" "}},
		"parent twice": {BuildCache: []string{"a/../../b"}},
	} {
		if err := o.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if d, err := CleanBuildCacheDir("./build/"); err != nil || d != "build" {
		t.Fatal(d, err)
	}
}

func TestWorktreeHooksAndCompose(t *testing.T) {
	h := WorktreeHooks{PostCreate: "a", PreSession: "b", PostIntegrate: "c", PreRemove: "d"}
	for phase, want := range map[string]string{HookPostCreate: "a", HookPreSession: "b", HookPostIntegrate: "c", HookPreRemove: "d", "other": ""} {
		if got := h.Command(phase); got != want {
			t.Errorf("%s = %q", phase, got)
		}
	}
	if (WorktreeIsolation{}).ComposeMode() != "auto" || (WorktreeIsolation{Compose: "off"}).ComposeMode() != "off" {
		t.Fatal("compose mode")
	}
	v := IsolationVars{Port: 1, Compose: "c", Item: "i1"}
	if env := v.BaseEnv(); env["BARNRAISE_PORTS"] != "" || env["PORT"] != "1" {
		t.Fatal(env)
	}
}
