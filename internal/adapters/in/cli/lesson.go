package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// lessonCmd lists and deletes the lessons Barnraise learned from rework.
func (e *env) lessonCmd() *cobra.Command {
	c := &cobra.Command{Use: "lesson", Aliases: []string{"lessons"}, Short: "Lessons learned from rework: list or delete them"}
	var file, area string
	list := &cobra.Command{Use: "list", Short: "List the lessons, the most taught first", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			q := url.Values{}
			if file != "" {
				q.Set("file", file)
			}
			if area != "" {
				q.Set("area", area)
			}
			var res struct {
				Lessons []*domain.Lesson `json:"lessons"`
			}
			if err := e.lessonCall(ctx, http.MethodGet, "/v1/lessons", q, &res); err != nil {
				return err
			}
			if e.json {
				e.printAny(res)
				return nil
			}
			if len(res.Lessons) == 0 {
				fmt.Fprintln(e.out, "No lessons yet.")
				return nil
			}
			for _, l := range res.Lessons {
				fmt.Fprintf(e.out, "%s  %s\n", l.ID, l.Text)
				if len(l.Files) > 0 {
					fmt.Fprintf(e.out, "    files: %s\n", strings.Join(l.Files, ", "))
				}
				fmt.Fprintf(e.out, "    from: %s\n", strings.Join(l.Sources, ", "))
			}
			return nil
		}}
	list.Flags().StringVar(&file, "file", "", "only the lessons about this file (and its folder)")
	list.Flags().StringVar(&area, "area", "", "only the lessons about this area")
	del := &cobra.Command{Use: "delete <id>", Aliases: []string{"rm"}, Short: "Delete a lesson: later prompts no longer get it", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := e.ctx()
			defer cancel()
			var l domain.Lesson
			if err := e.lessonCall(ctx, http.MethodDelete, "/v1/lessons/"+url.PathEscape(args[0]), url.Values{}, &l); err != nil {
				return err
			}
			if e.json {
				e.printAny(l)
				return nil
			}
			fmt.Fprintf(e.out, "Deleted %s.\n", l.ID)
			return nil
		}}
	c.AddCommand(list, del)
	return c
}

// lessonCall calls the daemon's lesson endpoints with the CLI's token.
func (e *env) lessonCall(ctx context.Context, method, path string, q url.Values, out any) error {
	cl := e.client()
	if p := e.proj(ctx); p != "" {
		q.Set("project", p)
	}
	u := cl.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	if cl.Token != "" {
		r.Header.Set("Authorization", "Bearer "+cl.Token)
	}
	res, err := cl.HTTP.Do(r)
	if err != nil {
		return fmt.Errorf("the daemon is not reachable (`barnraise up` starts it): %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("daemon answered %s", res.Status)
	}
	return json.Unmarshal(body, out)
}
