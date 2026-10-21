// Package bundled is the shipped template catalog: the Compose YAML files under this directory, embedded into the
// daemon binary so a fresh panel has a catalog before it has a filesystem to read one from.
//
// It is a separate package because go:embed cannot reach up out of its own directory: internal/modules/templates
// needs this data, and the data has to sit at the repository root where the plan puts it.
package bundled

import "embed"

// Compose holds every bundled template file under compose/. The catalog parser reads the whole directory, so a
// template is shipped by dropping a file here and nothing else changes.
//
// The metadata header format and the magic variable names this panel understands are documented in
// internal/modules/templates/parser.go.
//
//go:embed compose
var Compose embed.FS

// Dir is the directory inside Compose that holds the YAML files.
const Dir = "compose"
