// Package domain implements the isolated One-shot Agent domain model.
//
// It contains only pure aggregates, immutable value objects, stable error
// codes, and explicit state transitions. It intentionally has no database,
// HTTP, channel, process, PTY, or Interactive Session dependency.
package domain
