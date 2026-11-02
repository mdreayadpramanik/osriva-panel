// Package openapi embeds the OSRIVA Core API specification.
package openapi

import _ "embed"

//go:embed openapi.yaml
var Spec []byte
