// Package version resolves the user-visible Lore release version.
package version

import (
	"runtime/debug"
	"slices"
	"strings"
)

// releaseVersion is set at link time by GoReleaser. Renaming this identifier
// or moving this package breaks release stamping silently, so keep the
// link-time target in .goreleaser.yml in step with this declaration.
var releaseVersion string

// String returns the release version embedded by GoReleaser or the Go
// toolchain, without a leading "v".
func String() string {
	if releaseVersion != "" {
		return strings.TrimPrefix(releaseVersion, "v")
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}

	return fromBuildInfo(info)
}

func fromBuildInfo(info *debug.BuildInfo) string {
	// a stamped vcs key means the binary came from a working tree, so
	// Main.Version is toolchain-derived rather than a published release.
	local := slices.ContainsFunc(info.Settings, func(setting debug.BuildSetting) bool {
		return setting.Key == "vcs"
	})
	if local || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "devel"
	}
	return strings.TrimPrefix(info.Main.Version, "v")
}
