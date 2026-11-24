package cli

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/adapters/out/keychain"
	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// gitCredentialCmd is a git credential helper: it answers with the token
// kept in this machine's keychain for one repository (`barnraise machine
// share --token-stdin` stores it), and says nothing for any other, so git
// asks its next helper. Only get is answered; store and erase are ignored.
func (e *env) gitCredentialCmd() *cobra.Command {
	return &cobra.Command{Use: "git-credential <get|store|erase>", Hidden: true, Args: cobra.ExactArgs(1),
		Short: "Git credential helper for repositories whose token Barnraise keeps",
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "get" {
				_, _ = io.Copy(io.Discard, e.in)
				return nil
			}
			host, path := readCredentialRequest(e.in)
			key := domain.GitTokenSecret(host, path)
			if key == "" {
				return nil
			}
			tok, err := keychain.New("barnraise", filepath.Join(paths.ConfigDir(), "secrets")).Get(key)
			if err != nil || tok == "" {
				return nil
			}
			fmt.Fprintf(e.out, "username=x-access-token\npassword=%s\n", tok)
			return nil
		}}
}

// readCredentialRequest reads git's request: key=value lines up to a blank one.
func readCredentialRequest(r io.Reader) (host, path string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "host":
			host = v
		case "path":
			path = v
		}
	}
	return host, path
}
