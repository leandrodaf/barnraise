package main

import (
	"encoding/json"
	"io"
	"net/http"
)

func decodeJSON(res *http.Response, v any) error {
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
}
