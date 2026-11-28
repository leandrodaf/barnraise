package httpapi

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/app"
)

//go:embed dictation.py
var dictationWorker string

func (s *Server) routeDictation() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	python := filepath.Join(home, ".local", "share", "barnraise-dictation", "venv", "bin", "python")
	slot := make(chan struct{}, 1)
	handle := func(w http.ResponseWriter, r *http.Request) {
		if actorFrom(r.Context()).Kind != app.ActorOwner {
			writeErr(w, 403, "owner required")
			return
		}
		_, err := os.Stat(python)
		if r.Method == "GET" {
			writeJSON(w, map[string]bool{"available": err == nil})
			return
		}
		if err != nil {
			writeErr(w, 503, "Local transcription unavailable")
			return
		}
		select {
		case slot <- struct{}{}:
			defer func() { <-slot }()
		default:
			writeErr(w, 429, "Transcription busy")
			return
		}
		format := map[string]string{"audio/webm": "webm", "audio/mp4": "mov", "audio/ogg": "ogg", "audio/wav": "wav"}[strings.Split(r.Header.Get("Content-Type"), ";")[0]]
		if format == "" {
			writeErr(w, 415, "Unsupported recording format")
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil || len(data) == 0 {
			writeErr(w, 400, "Invalid recording")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, python, "-I", "-c", dictationWorker, format)
		cmd.Stdin = bytes.NewReader(data)
		output, err := cmd.Output()
		if err != nil {
			writeErr(w, 422, "Local transcription failed")
			return
		}
		writeJSON(w, map[string]string{"text": strings.TrimSpace(string(output))})
	}
	for _, method := range []string{"GET", "POST"} {
		s.mux.Handle(method+" /v1/dictation", s.auth(http.HandlerFunc(handle)))
	}
}
