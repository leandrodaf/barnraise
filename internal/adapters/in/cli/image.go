package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/adapters/out/imagex"
	"github.com/leandrodaf/barnraise/internal/domain"
)

func (e *env) imageCmd() *cobra.Command {
	c := &cobra.Command{Use: "image", Short: "Prints for agents: reduced by default, full-resolution regions on demand"}
	var width int
	var out string
	reduce := &cobra.Command{Use: "reduce <file>", Short: "Write a copy at most --width pixels wide (default 540)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := out
			if dir == "" {
				dir = filepath.Join(filepath.Dir(args[0]), ".reduced")
			}
			p, reduced, err := imagex.ReduceFile(args[0], dir, width)
			if err != nil {
				return err
			}
			if !reduced {
				fmt.Fprintf(e.out, "%s is already at most %d px wide.\n", args[0], width)
				return nil
			}
			fmt.Fprintln(e.out, p)
			return nil
		}}
	reduce.Flags().IntVar(&width, "width", imagex.DefaultWidth, "maximum width (540 by default: ~44% fewer tokens than a full phone print)")
	reduce.Flags().StringVar(&out, "out", "", "output folder")
	crop := &cobra.Command{Use: "crop <file> <x,y,w,h>", Short: "Cut a full-resolution region (flagged screens)", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			parts := strings.Split(args[1], ",")
			if len(parts) != 4 {
				return fmt.Errorf("region must be x,y,w,h")
			}
			var v [4]int
			for i, p := range parts {
				n, err := strconv.Atoi(strings.TrimSpace(p))
				if err != nil || n < 0 {
					return fmt.Errorf("region must be x,y,w,h with non-negative integers")
				}
				v[i] = n
			}
			base := os.Getenv("BARNRAISE_WORKTREE")
			if base == "" {
				base = filepath.Dir(args[0])
			}
			dst := filepath.Join(base, ".barnraise-session", "shots", "full", domain.Slug(filepath.Base(args[0]), 40)+"-"+strings.ReplaceAll(args[1], ",", "_")+".png")
			if err := imagex.Crop(args[0], dst, v[0], v[1], v[2], v[3]); err != nil {
				return err
			}
			fmt.Fprintln(e.out, dst)
			return nil
		}}
	var costWidth int
	cost := &cobra.Command{Use: "cost <folder>", Short: "Tokens the images of a folder cost an agent, full and reduced", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := imagex.MeasureDir(args[0], costWidth)
			if err != nil {
				return err
			}
			if e.json {
				e.printAny(c)
				return nil
			}
			if c.Images == 0 {
				fmt.Fprintln(e.out, "No images.")
				return nil
			}
			fmt.Fprintf(e.out, "%d images: %d tokens at full resolution, %d reduced to %d px (%.1f%% saved); %d and %d per image.\n",
				c.Images, c.FullTokens, c.ReducedTok, c.Width, c.SavedPct, c.FullTokens/c.Images, c.ReducedTok/c.Images)
			return nil
		}}
	cost.Flags().IntVar(&costWidth, "width", imagex.DefaultWidth, "width the agents read")
	c.AddCommand(reduce, crop, cost)
	return c
}
