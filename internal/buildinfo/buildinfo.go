// Package buildinfo exposes version and compatibility metadata for hydrus-go.
package buildinfo

import "fmt"

const (
	// ClientAPIVersion tracks the current Hydrus Client API compatibility target.
	ClientAPIVersion = 90
	// ReferenceHydrusVersion tracks the Hydrus release used as the current
	// migration compatibility reference point.
	ReferenceHydrusVersion = 668
	// DefaultCompatibilityTag identifies this implementation in compatibility-
	// oriented server metadata.
	DefaultCompatibilityTag = "hydrus-go"
)

var (
	// Version is the build version injected at compile time when available.
	Version = "dev"
	// Commit is the source revision injected at compile time when available.
	Commit = ""
	// BuiltAt is the build timestamp injected at compile time when available.
	BuiltAt = ""
)

// ServerHeader returns the compatibility-oriented server header used by the
// bootstrap HTTP API.
func ServerHeader() string {
	return fmt.Sprintf(
		"client api/%d (%s %s)",
		ClientAPIVersion,
		DefaultCompatibilityTag,
		Version,
	)
}
