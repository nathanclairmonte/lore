package version

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStringUsesInjectedReleaseVersion(t *testing.T) {
	previous := releaseVersion
	releaseVersion = "v0.1.0"
	t.Cleanup(func() {
		releaseVersion = previous
	})

	require.Equal(t, "0.1.0", String())
}

func TestFromBuildInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{name: "tagged module", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, want: "0.1.0"},
		{name: "module pseudo-version", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260812155822-96749feb0649"}}, want: "0.0.0-20260812155822-96749feb0649"},
		{name: "local checkout", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}, Settings: []debug.BuildSetting{{Key: "vcs", Value: "git"}}}, want: "devel"},
		{name: "unversioned module", info: &debug.BuildInfo{Main: debug.Module{Version: ""}}, want: "devel"},
		{name: "development module", info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, want: "devel"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.want, fromBuildInfo(test.info))
		})
	}
}
