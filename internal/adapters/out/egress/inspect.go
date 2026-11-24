package egress

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/leandrodaf/barnraise/internal/adapters/out/secretscan"
)

// MaxInspectBody is the largest request body the proxy holds in memory to
// scan. A larger body is passed on unscanned, and its receipt says so.
var MaxInspectBody int64 = 32 << 20

// credentialHeader reports headers that carry the caller's own credential
// for the destination (a GitHub token to GitHub is not a leak): they are
// not scanned. Everything else of the request is.
func credentialHeader(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"authorization", "cookie", "token", "key", "secret", "session", "auth"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// scanRequest looks for secrets in the URL, the headers that are not
// credentials, and the body (decompressed when gzip).
func scanRequest(u *url.URL, h http.Header, body []byte, gz bool) []secretscan.Finding {
	var lines bytes.Buffer
	lines.WriteString(u.String())
	lines.WriteByte('\n')
	if raw, err := url.PathUnescape(u.RawQuery); err == nil && raw != "" {
		lines.WriteString(strings.ReplaceAll(raw, "&", "\n"))
		lines.WriteByte('\n')
	}
	for k, vs := range h {
		if credentialHeader(k) {
			continue
		}
		for _, v := range vs {
			lines.WriteString(k + ": " + v + "\n")
		}
	}
	files := map[string][]byte{"request": lines.Bytes()}
	if gz {
		if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			if plain, err := io.ReadAll(io.LimitReader(zr, MaxInspectBody)); err == nil {
				body = plain
			}
		}
	}
	if len(body) > 0 {
		files["body"] = body
	}
	return secretscan.Scan(files)
}
