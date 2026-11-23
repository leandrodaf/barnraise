package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	pb "github.com/leandrodaf/barnraise/gen/barnraise/v1"
	"github.com/leandrodaf/barnraise/internal/adapters/out/codex"
	"github.com/leandrodaf/barnraise/internal/config"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// APIPlan marks an account of a key-based provider: it has an API key
// instead of a login, and no 5-hour or weekly window.
const APIPlan = "api"

// apiProviders are the key-based providers this command can add accounts
// for: the built-in ones plus those of the barnraise.yaml of the current
// directory, when there is one.
func apiProviders() map[string]domain.APIProviderDef {
	if root, err := findRoot(); err == nil {
		if cfg, err := config.Load(root); err == nil {
			return cfg.APIProviders
		}
	}
	return domain.DefaultAPIProviders()
}

func providerChoices(apis map[string]domain.APIProviderDef) string {
	names := []string{"claude", "codex", "gemini"}
	keys := make([]string, 0, len(apis))
	for k := range apis {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(append(names, keys...), ", ")
}

// readAPIKey reads one line from in. The key is never taken from a flag:
// a command line ends up in the shell history and in the process list.
func readAPIKey(in io.Reader, out io.Writer, provider string) (string, error) {
	fmt.Fprintf(out, "API key for %s: ", provider)
	sc := bufio.NewScanner(in)
	key := ""
	if sc.Scan() {
		key = strings.TrimSpace(sc.Text())
	}
	fmt.Fprintln(out)
	if key == "" {
		return "", errors.New("no API key given")
	}
	return key, nil
}

// storeAPIAccount puts the key in the keychain under the account's own
// key and returns the account for the rotation. The account's folder (the
// CLI's home for its sessions) is created empty: the key is never written
// there, nor in barnraise.yaml or the event log.
func storeAPIAccount(ks ports.Secrets, id, provider, key, dir, machine string) (*pb.Account, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := ks.Set(domain.APIKeySecret(id), key); err != nil {
		return nil, fmt.Errorf("store the API key: %w", err)
	}
	// No windows to keep a share of: the spending limit belongs on the key,
	// at the provider. The ceiling stays at 100 so nothing reads it as a cap.
	return &pb.Account{Id: id, Provider: provider, Plan: APIPlan, ConfigDir: dir, CeilingPct: 100, Status: "ok", Machine: machine}, nil
}

// testAPICommand is the minimal question that proves the key and the model
// answer through the endpoint, with the same codex overrides as a session.
func testAPICommand(ctx context.Context, ep ports.Endpoint, model, dir, key string) *exec.Cmd {
	args := []string{"exec", "--skip-git-repo-check", "--sandbox", "read-only", "--model", model}
	args = append(args, codex.EndpointArgs(ep)...)
	cmd := exec.CommandContext(ctx, "codex", append(args, "-")...)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+dir, "DISABLE_AUTOUPDATER=1", ports.EndpointKeyEnv+"="+key)
	cmd.Stdin = strings.NewReader("Reply with the single word OK.")
	return cmd
}

func testAPIAccount(ep ports.Endpoint, model, dir, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := testAPICommand(ctx, ep, model, dir, key).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, tailStr(string(out), 400))
	}
	if !strings.Contains(strings.ToUpper(string(out)), "OK") {
		return fmt.Errorf("unexpected answer: %s", tailStr(string(out), 200))
	}
	return nil
}
