// Package httpapi is the daemon's public API: one Connect service answering
// gRPC and HTTP with JSON, plus live events (SSE, CloudEvents), inbound
// webhooks, the OpenAPI document and the web panel.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/leandrodaf/barnraise/internal/app"
)

// The proto messages mirror the daemon's JSON (same snake_case names), so
// conversion goes through JSON: one contract, no hand-written mapping.

var unmarshalPB = protojson.UnmarshalOptions{DiscardUnknown: true}
var marshalPB = protojson.MarshalOptions{UseProtoNames: true}

// ToPB fills a proto message from any JSON-tagged value.
func ToPB(src any, dst proto.Message) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	if string(data) == "null" {
		return nil
	}
	return unmarshalPB.Unmarshal(data, dst)
}

// FromPB fills a JSON-tagged value from a proto message.
func FromPB(src proto.Message, dst any) error {
	data, err := marshalPB.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

// ToStruct converts any value to a protobuf Struct.
func ToStruct(v any) (*structpb.Struct, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		// Not an object: wrap it.
		var anyv any
		_ = json.Unmarshal(data, &anyv)
		m = map[string]any{"value": anyv}
	}
	return structpb.NewStruct(m)
}

// FromStruct converts a protobuf Struct into a JSON-tagged value.
func FromStruct(s *structpb.Struct, dst any) error {
	if s == nil {
		return nil
	}
	data, err := s.MarshalJSON()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

// connectErr maps application errors to Connect codes.
func connectErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*connect.Error](err); ok {
		return err
	}
	code := connect.CodeInternal
	switch {
	case errors.Is(err, app.ErrNotFound), errors.Is(err, app.ErrUnknownProj):
		code = connect.CodeNotFound
	case errors.Is(err, app.ErrForbidden):
		code = connect.CodePermissionDenied
	case errors.Is(err, app.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, app.ErrConflict):
		code = connect.CodeFailedPrecondition
	case errors.Is(err, app.ErrPaused):
		code = connect.CodeUnavailable
	case errors.Is(err, app.ErrNoWork):
		code = connect.CodeNotFound
	}
	return connect.NewError(code, err)
}

// snakeJSON is the Connect JSON codec with the proto field names (snake_case),
// the same names the daemon uses everywhere (CLI --json, events, docs).
type snakeJSON struct{}

func (snakeJSON) Name() string { return "json" }

func (snakeJSON) Marshal(m any) ([]byte, error) {
	pm, ok := m.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("json codec: %T is not a proto message", m)
	}
	return protojson.MarshalOptions{UseProtoNames: true}.Marshal(pm)
}

func (snakeJSON) Unmarshal(data []byte, m any) error {
	pm, ok := m.(proto.Message)
	if !ok {
		return fmt.Errorf("json codec: %T is not a proto message", m)
	}
	if len(data) == 0 {
		return nil
	}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, pm)
}
