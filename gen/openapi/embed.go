// Package openapi embeds the OpenAPI document generated from the protobuf contract.
package openapi

import _ "embed"

//go:embed barnraise/v1/barnraise.openapi.yaml
var doc []byte

// Document returns the OpenAPI YAML.
func Document() []byte { return doc }
