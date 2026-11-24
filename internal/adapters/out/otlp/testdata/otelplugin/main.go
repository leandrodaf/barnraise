// Command otelplugin runs the OTel hook plugin as the daemon would start
// `barnraise plugin otel`, for the protocol test with the real plugin host.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/leandrodaf/barnraise/internal/adapters/out/otlp"
)

func main() {
	if err := otlp.Serve(context.Background(), os.Stdin, os.Stdout, otlp.ServeOptions{Version: "test"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
