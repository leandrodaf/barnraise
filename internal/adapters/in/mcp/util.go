package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/leandrodaf/barnraise/internal/client"
)

// tool is one MCP tool: its contract and the code that runs it.
type tool struct {
	Name        string
	Description string
	Schema      map[string]any
	run         func(ctx context.Context, s *server, args json.RawMessage) *toolResult
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content           []content `json:"content"`
	StructuredContent any       `json:"structuredContent,omitempty"`
	IsError           bool      `json:"isError,omitempty"`
}

func textResult(text string, structured any) *toolResult {
	return &toolResult{Content: []content{{Type: "text", Text: strings.TrimSpace(text)}}, StructuredContent: structured}
}

func errorResult(msg string) *toolResult {
	msg = strings.TrimSpace(msg)
	if msg != "" && !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	return &toolResult{Content: []content{{Type: "text", Text: msg}}, IsError: true}
}

// daemonError turns an error of the daemon into a tool error.
func daemonError(err error) *toolResult {
	if errors.Is(err, context.Canceled) {
		return errorResult("the call was cancelled")
	}
	return errorResult("Barnraise refused the call: " + client.Friendly(err).Error())
}

// pbJSON renders a protobuf message as a JSON object with the snake_case names.
func pbJSON(m proto.Message) json.RawMessage {
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil || len(b) == 0 {
		return json.RawMessage("{}")
	}
	// Compact it: protojson may add spaces at random.
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return json.RawMessage(b)
	}
	return json.RawMessage(buf.Bytes())
}

// ---------- schemas ----------

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func integer(desc string, minimum, maximum int) map[string]any {
	m := map[string]any{"type": "integer", "description": desc, "minimum": minimum}
	if maximum > minimum {
		m["maximum"] = maximum
	}
	return m
}

func array(desc string, items map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": items}
}

func strList(desc string) map[string]any { return array(desc, map[string]any{"type": "string"}) }

// object is a strict object schema: only the listed properties are accepted.
func object(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

// minMax bounds the length of an array schema.
func minMax(m map[string]any, minItems, maxItems int) map[string]any {
	m["minItems"] = minItems
	m["maxItems"] = maxItems
	return m
}

// ---------- arguments ----------

// decodeArgs decodes the arguments strictly into v and explains problems in
// plain sentences the model can act on.
func decodeArgs(t *tool, args json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		return nil
	}
	var te *json.UnmarshalTypeError
	var se *json.SyntaxError
	switch {
	case errors.As(err, &te):
		if te.Field == "" {
			return errors.New("arguments must be a JSON object")
		}
		return fmt.Errorf("argument %s must be %s, not a JSON %s", te.Field, kindName(te.Type), te.Value)
	case errors.As(err, &se):
		return errors.New("arguments are not valid JSON")
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		name := strings.TrimPrefix(err.Error(), "json: unknown field ")
		allowed := propNames(t.Schema)
		if len(allowed) == 0 {
			return fmt.Errorf("unknown argument %s: %s takes no arguments", name, t.Name)
		}
		return fmt.Errorf("unknown argument %s: %s accepts %s", name, t.Name, strings.Join(allowed, ", "))
	}
	return fmt.Errorf("invalid arguments: %v", err)
}

func kindName(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int32, reflect.Int64:
		return "an integer"
	case reflect.Slice:
		return "a list"
	case reflect.Struct, reflect.Map:
		return "an object"
	}
	return "a " + t.Kind().String()
}

func propNames(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	out := make([]string, 0, len(props))
	for k := range props {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

// missing returns an error naming the required fields that are empty.
func missing(tool string, fields ...[2]string) error {
	var names []string
	for _, f := range fields {
		if blank(f[1]) {
			names = append(names, f[0])
		}
	}
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf("%s needs %s", tool, strings.Join(names, ", "))
}

func oneOf(v string, values ...string) bool {
	return slices.Contains(values, v)
}
