// Command testplugin is the plugin the host's contract tests run: it
// implements every kind of docs/plugins.md in memory, plus a few ways to
// misbehave chosen by flags.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

type msg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var (
	mode  = flag.String("mode", "", "badjson, oldproto, silent, noname")
	kinds = flag.String("kinds", "tracker,check,notifier,hook", "kinds to declare")

	wmu sync.Mutex
	smu sync.Mutex

	entries   = map[string]map[string]any{}
	order     []string
	comments  []map[string]string
	notes     []map[string]any
	events    []json.RawMessage
	cancelled = map[string]chan struct{}{}
)

func send(m msg) {
	m.JSONRPC = "2.0"
	b, _ := json.Marshal(m)
	wmu.Lock()
	defer wmu.Unlock()
	os.Stdout.Write(append(b, '\n'))
}

func main() {
	flag.Parse()
	if f := os.Getenv("TESTPLUGIN_STARTS"); f != "" {
		fh, _ := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintln(fh, "start")
		fh.Close()
	}
	fmt.Fprintln(os.Stderr, "testplugin up")
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m msg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			send(msg{ID: json.RawMessage("null"), Error: &rpcErr{Code: -32700, Message: err.Error()}})
			continue
		}
		if m.Method == "$/cancel" {
			var p struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(m.Params, &p)
			smu.Lock()
			if ch, ok := cancelled[string(p.ID)]; ok {
				close(ch)
				delete(cancelled, string(p.ID))
			}
			smu.Unlock()
			continue
		}
		if len(m.ID) == 0 {
			continue
		}
		if m.Method == "initialize" {
			initialize(m)
			continue
		}
		if m.Method == "shutdown" {
			send(msg{ID: m.ID, Result: map[string]any{}})
			continue
		}
		if *mode == "badjson" {
			os.Stdout.WriteString("this is not json\n")
			continue
		}
		ch := make(chan struct{})
		smu.Lock()
		cancelled[string(m.ID)] = ch
		smu.Unlock()
		go func() {
			res, e := handle(m, ch)
			smu.Lock()
			delete(cancelled, string(m.ID))
			smu.Unlock()
			if e != nil {
				send(msg{ID: m.ID, Error: e})
				return
			}
			if res == nil {
				res = map[string]any{}
			}
			send(msg{ID: m.ID, Result: res})
		}()
	}
}

func initialize(m msg) {
	switch *mode {
	case "silent":
		return
	case "oldproto":
		send(msg{ID: m.ID, Result: map[string]any{"name": "testplugin", "version": "0.0.1", "protocol_version": 99, "kinds": []string{"tracker"}}})
		return
	case "noname":
		send(msg{ID: m.ID, Result: map[string]any{"version": "0.0.1", "protocol_version": 1, "kinds": []string{"tracker"}}})
		return
	}
	var p struct {
		Options map[string]any `json:"options"`
	}
	_ = json.Unmarshal(m.Params, &p)
	if p.Options["greeting"] != nil {
		send(msg{Method: "$/log", Params: mustJSON(map[string]string{"level": "warn", "message": fmt.Sprint(p.Options["greeting"])})})
	}
	send(msg{ID: m.ID, Result: map[string]any{"name": "testplugin", "version": "0.0.1", "protocol_version": 1, "kinds": kindList()}})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func handle(m msg, cancel chan struct{}) (any, *rpcErr) {
	if m.Method == "check.run" {
		return runCheck(m, cancel)
	}
	smu.Lock()
	defer smu.Unlock()
	switch m.Method {
	case "tracker.columns":
		return map[string]any{"columns": []string{"Todo", "Doing", "Done"}}, nil
	case "tracker.create":
		var p struct {
			Item map[string]any `json:"item"`
		}
		_ = json.Unmarshal(m.Params, &p)
		ref := fmt.Sprintf("T-%d", len(order)+1)
		p.Item["ref"] = ref
		entries[ref] = p.Item
		order = append(order, ref)
		return map[string]any{"ref": ref, "title": p.Item["title"], "status": p.Item["column"], "url": "https://tracker.example/" + ref}, nil
	case "tracker.update":
		var p struct {
			Item map[string]any `json:"item"`
		}
		_ = json.Unmarshal(m.Params, &p)
		ref, _ := p.Item["ref"].(string)
		if _, ok := entries[ref]; !ok {
			return nil, &rpcErr{Code: -32001, Message: "no entry " + ref}
		}
		entries[ref] = p.Item
		return nil, nil
	case "tracker.comment":
		var p map[string]string
		_ = json.Unmarshal(m.Params, &p)
		comments = append(comments, p)
		return nil, nil
	case "tracker.list":
		var out []map[string]any
		for _, ref := range order {
			e := entries[ref]
			out = append(out, map[string]any{"ref": ref, "title": e["title"], "body": e["body"], "status": e["column"]})
		}
		return map[string]any{"items": out}, nil
	case "notify.send":
		var p map[string]any
		_ = json.Unmarshal(m.Params, &p)
		notes = append(notes, p)
		return nil, nil
	case "hook.event":
		var p struct {
			Event json.RawMessage `json:"event"`
		}
		_ = json.Unmarshal(m.Params, &p)
		events = append(events, p.Event)
		return nil, nil
	case "debug.state":
		return map[string]any{"entries": entries, "comments": comments, "notes": notes, "events": events}, nil
	case "debug.env":
		return map[string]any{"env": os.Environ()}, nil
	}
	return nil, &rpcErr{Code: -32601, Message: "method not found: " + m.Method}
}

func runCheck(m msg, cancel chan struct{}) (any, *rpcErr) {
	var p struct {
		Run string            `json:"run"`
		Env map[string]string `json:"env"`
	}
	_ = json.Unmarshal(m.Params, &p)
	switch p.Run {
	case "crash":
		os.Exit(3)
	case "hang":
		time.Sleep(time.Minute)
	case "hang-cancel":
		select {
		case <-cancel:
			return nil, &rpcErr{Code: -32800, Message: "cancelled"}
		case <-time.After(time.Minute):
		}
	case "fail":
		return map[string]any{"passed": false, "summary": "2 tests failed", "log": "--- FAIL: TestA\n--- FAIL: TestB\n", "artifacts": []string{"report.xml"}}, nil
	case "pending":
		return map[string]any{"passed": false, "pending": true, "summary": "queued on the device farm"}, nil
	case "error":
		return nil, &rpcErr{Code: -32603, Message: "the device farm is down"}
	}
	return map[string]any{"passed": true, "summary": "all good on " + p.Env["PORT"], "duration_ms": 1500}, nil
}

func kindList() []string {
	out := []string{}
	for _, k := range strings.Split(*kinds, ",") {
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}
