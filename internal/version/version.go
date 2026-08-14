// Package version exposes build-time version information.
package version

import "fmt"

var (
	// Version is set via -ldflags at build time.
	Version = "dev"
	// Commit is the git revision at build time.
	Commit = "none"
	// Date is the UTC build timestamp.
	Date = "unknown"
)

// String returns the full human-readable version line.
func String() string {
	return fmt.Sprintf("k8s-ai %s (commit %s, built %s)", Version, Commit, Date)
}
