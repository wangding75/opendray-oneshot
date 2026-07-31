//go:build local_onnx

package memory

import "os"

// openReadOnly is a tiny wrapper around os.Open kept in its own
// file so embedder_onnx.go doesn't need to import "os" — keeps the
// non-cgo build (without local_onnx tag) free of unused imports.
func openReadOnly(p string) (*os.File, error) { return os.Open(p) }
