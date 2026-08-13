// Package version holds the build-time version shared by every Soulstream binary.
package version

// Version is "dev" for source builds; release builds overwrite it via
//
//	-ldflags "-X github.com/impire-io/soulstream-core/internal/version.Version=<semver>"
var Version = "dev"
