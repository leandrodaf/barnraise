package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/adapters/out/execx"
	"github.com/leandrodaf/barnraise/internal/domain"
)

// tarball builds an npm package archive with one file.
func tarball(t *testing.T, name, version, body string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for file, content := range map[string]string{
		"package/package.json": `{"name":"` + name + `","version":"` + version + `","main":"index.js"}`,
		"package/index.js":     body,
	} {
		if err := tw.WriteHeader(&tar.Header{Name: file, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// The real thing: two items, two worktrees, the project's real setup command
// (pnpm install) run by the real executor, and one store. The dependency's
// file is the same file on disk in both worktrees and in the store: a second
// worktree costs directories and links, not another copy of the dependencies.
// The dependency is a tarball in the repository, so nothing is downloaded.
func TestSharedStoreWithRealPnpm(t *testing.T) {
	if testing.Short() {
		t.Skip("runs pnpm")
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm is not installed")
	}
	const body = "module.exports = 'the same bytes in every worktree'\n"
	e := newEnv(t, withGit(), withFiles(map[string]string{
		"package.json":          `{"name":"demo","version":"1.0.0","private":true,"dependencies":{"shared-dep":"file:vendor/shared-dep.tgz"}}`,
		"vendor/shared-dep.tgz": tarball(t, "shared-dep", "1.0.0", body),
		".gitignore":            "node_modules/\n",
	}), withCfg(func(c *domain.ProjectConfig) {
		// Hard links, as on ext4: on a filesystem that clones, pnpm would
		// share blocks instead and the files would not be the same inode.
		c.WorktreeSetup = "pnpm install --offline --ignore-scripts --package-import-method=hardlink"
		c.WorktreeStores = map[string]string{"pnpm": "npm_config_store_dir"}
	}))
	e.svc.d.Exec = execx.New()

	a, b := e.ready("simple", "one", "x"), e.ready("simple", "two", "x")
	e.start(a.ID)
	e.start(b.ID)
	dirA, dirB := e.workspace(a.ID, nil), e.workspace(b.ID, nil)

	installed := func(dir string) os.FileInfo {
		t.Helper()
		path := filepath.Join(dir, "node_modules", "shared-dep", "index.js")
		got, err := os.ReadFile(path)
		if err != nil || string(got) != body {
			t.Fatalf("the dependency is not installed in %s: %v %q", dir, err, got)
		}
		fi, err := os.Stat(path) // follows pnpm's link into node_modules/.pnpm
		if err != nil {
			t.Fatal(err)
		}
		return fi
	}
	fa, fb := installed(dirA), installed(dirB)
	if !os.SameFile(fa, fb) {
		t.Fatal("each worktree has its own copy of the dependency: the store is not shared")
	}
	// And that one file is the store's: it lives in the project's store directory.
	store := filepath.Join(e.svc.d.DataDir, "stores", "demo", "pnpm")
	inStore := false
	_ = filepath.WalkDir(store, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || inStore {
			return nil
		}
		if fi, err := d.Info(); err == nil && os.SameFile(fi, fa) {
			inStore = true
		}
		return nil
	})
	if !inStore {
		t.Fatalf("the installed file is not the one in %s", store)
	}
	// Nothing of it is offered to git, in either worktree.
	for _, dir := range []string{dirA, dirB} {
		out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out, []byte("node_modules")) {
			t.Fatalf("git sees the dependencies in %s: %s", dir, out)
		}
	}
}

// The store reaches the agent's container: mounted where the environment
// says, writable by the agent's user, and it is the same directory as on
// the host. Runs a real container with the mounts containerSpec decides.
func TestSharedStoreInARealContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a container")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	image := ""
	for _, img := range []string{"busybox", "alpine", "postgres:17-alpine"} {
		if exec.Command("docker", "image", "inspect", img).Run() == nil { // never pulls
			image = img
			break
		}
	}
	if image == "" {
		t.Skip("no small image on this machine")
	}
	e := newEnv(t, withGit(), withCfg(func(c *domain.ProjectConfig) {
		c.WorktreeStores = map[string]string{"pnpm": "npm_config_store_dir"}
	}))
	it := e.ready("simple", "one", "x")
	e.start(it.ID)
	dir := e.workspace(it.ID, nil)
	env := e.svc.StoreEnv(e.p)
	store := env["npm_config_store_dir"]
	if err := os.WriteFile(filepath.Join(store, "from-host"), []byte("host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	acc := &domain.Account{ID: "a", ConfigDir: t.TempDir()}
	cs, err := e.svc.Runtime().containerSpec(e.p, domain.ContainerDef{Image: image}, dir, filepath.Join(dir, ".barnraise-session"), acc, "fake", env)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--rm", "--network", "none", "--user", cs.User, "--entrypoint", "sh", "--env", "npm_config_store_dir=" + store}
	for _, m := range cs.Mounts {
		path, ro := m, ""
		if p, ok := bytes.CutSuffix([]byte(m), []byte(":ro")); ok {
			path, ro = string(p), ":ro"
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		args = append(args, "--volume", path+":"+path+ro)
	}
	args = append(args, image, "-c", `cat "$npm_config_store_dir/from-host" && echo container > "$npm_config_store_dir/from-container"`)
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("the container could not use the store: %v\n%s", err, out)
	}
	if string(out) != "host\n" {
		t.Fatalf("the container read %q from the store", out)
	}
	if got, err := os.ReadFile(filepath.Join(store, "from-container")); err != nil || string(got) != "container\n" {
		t.Fatalf("what the container wrote did not reach the host's store: %q %v", got, err)
	}
}

// gradleBinary finds a Gradle to run: on the PATH, or a distribution the
// Gradle wrapper already downloaded for some project of this machine.
func gradleBinary() string {
	if p, err := exec.LookPath("gradle"); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	found, _ := filepath.Glob(filepath.Join(home, ".gradle", "wrapper", "dists", "*", "*", "gradle-*", "bin", "gradle"))
	if len(found) == 0 {
		return ""
	}
	return found[len(found)-1]
}

// The same with Gradle: GRADLE_USER_HOME points at the project's store, so
// Gradle itself, run by the real setup in two worktrees, says its home is the
// store and writes its caches there once, not in each worktree and not in the
// owner's ~/.gradle. The build has no plugins or dependencies: nothing is
// downloaded.
func TestSharedStoreWithRealGradle(t *testing.T) {
	if testing.Short() {
		t.Skip("runs Gradle")
	}
	gradle := gradleBinary()
	if gradle == "" {
		t.Skip("Gradle is not installed")
	}
	if _, err := exec.LookPath("java"); err != nil {
		t.Skip("Java is not installed")
	}
	e := newEnv(t, withGit(), withFiles(map[string]string{
		"settings.gradle": "rootProject.name = 'demo'\n",
		"build.gradle": `tasks.register('stamp') {
    def home = gradle.gradleUserHomeDir.absolutePath
    def out = layout.buildDirectory.file('gradle-home.txt')
    outputs.file(out)
    doLast { out.get().asFile.text = home }
}
`,
		".gitignore": ".gradle/\nbuild/\n",
	}), withCfg(func(c *domain.ProjectConfig) {
		c.WorktreeSetup = "'" + gradle + "' --offline --no-daemon --quiet stamp"
		c.WorktreeSetupTimeout = 5 * time.Minute
		c.WorktreeStores = map[string]string{"gradle": "GRADLE_USER_HOME"}
	}))
	e.svc.d.Exec = execx.New()

	a, b := e.ready("simple", "one", "x"), e.ready("simple", "two", "x")
	e.start(a.ID)
	e.start(b.ID)
	store := filepath.Join(e.svc.d.DataDir, "stores", "demo", "gradle")
	for _, dir := range []string{e.workspace(a.ID, nil), e.workspace(b.ID, nil)} {
		got, err := os.ReadFile(filepath.Join(dir, "build", "gradle-home.txt"))
		if err != nil {
			t.Fatalf("Gradle did not run in %s: %v", dir, err)
		}
		if string(got) != store {
			t.Fatalf("Gradle in %s used %q as its home, want the shared store %s", dir, got, store)
		}
		if out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); err != nil || len(bytes.TrimSpace(out)) != 0 {
			t.Fatalf("git sees what Gradle left in %s: %s %v", dir, out, err)
		}
	}
	// Gradle's own caches are in the store, once for both worktrees.
	if ents, err := os.ReadDir(filepath.Join(store, "caches")); err != nil || len(ents) == 0 {
		t.Fatalf("Gradle wrote no caches in the store: %v", err)
	}
}
