package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/adapters/out/egress"
	"github.com/leandrodaf/barnraise/internal/paths"
)

// egressReceiptsCmd reads and verifies the proxy's signed receipts. It
// works on the files alone: no daemon needed.
func (e *env) egressReceiptsCmd() *cobra.Command {
	c := &cobra.Command{Use: "receipts", Short: "The egress proxy's signed log of allow/block decisions"}
	var file, pub string
	defaults := func() {
		dir := filepath.Join(paths.DataDir(), "egress")
		if file == "" {
			file = filepath.Join(dir, egress.ReceiptsLog)
		}
		if pub == "" {
			pub = filepath.Join(dir, egress.ReceiptsPubFile)
		}
	}
	verify := &cobra.Command{Use: "verify", Short: "Check every receipt's signature and the chain between them", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			defaults()
			n, err := verifyReceiptsFile(file, pub)
			if err != nil {
				return fmt.Errorf("%s: %d receipts valid, then: %w", file, n, err)
			}
			fmt.Fprintf(e.out, "✔ %d receipts verified (%s)\n", n, file)
			return nil
		}}
	var last int
	var blocks bool
	list := &cobra.Command{Use: "list", Short: "Show the latest receipts", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			defaults()
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			return e.listReceipts(f, last, blocks)
		}}
	for _, sc := range []*cobra.Command{verify, list} {
		sc.Flags().StringVar(&file, "file", "", "receipt log (default: the data dir's egress/"+egress.ReceiptsLog+")")
	}
	verify.Flags().StringVar(&pub, "pub", "", "public key (default: the data dir's egress/"+egress.ReceiptsPubFile+")")
	list.Flags().IntVar(&last, "last", 20, "how many receipts")
	list.Flags().BoolVar(&blocks, "blocked", false, "only blocks")
	c.AddCommand(verify, list)
	return c
}

func verifyReceiptsFile(file, pubPath string) (int, error) {
	pub, err := egress.ReadPublicKey(pubPath)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return egress.VerifyReceipts(f, pub)
}

func (e *env) listReceipts(r io.Reader, last int, blocks bool) error {
	var keep []egress.Receipt
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var rc egress.Receipt
		if json.Unmarshal(sc.Bytes(), &rc) != nil || (blocks && rc.Decision != "block") {
			continue
		}
		keep = append(keep, rc)
		if len(keep) > last {
			keep = keep[1:]
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if e.json {
		return json.NewEncoder(e.out).Encode(keep)
	}
	for _, rc := range keep {
		line := fmt.Sprintf("%6d %s %-5s %-7s %s%s  %s", rc.Seq, rc.Time.Format("2006-01-02 15:04:05"), rc.Decision, rc.Method, rc.Host, rc.Path, rc.Rule)
		if rc.Detail != "" {
			line += "  " + rc.Detail
		}
		fmt.Fprintln(e.out, line)
	}
	return nil
}
