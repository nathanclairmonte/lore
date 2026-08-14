package output

import (
	"encoding/json"
	"fmt"
)

// PrintVersion emits the resolved Lore build version.
//
// Human mode: `lore version <version>`.
//
// JSON mode: `{"version":"<version>"}`.
func PrintVersion(version string) {
	if mode == ModeJSON {
		_ = json.NewEncoder(Out).Encode(VersionEnvelope{Version: version})
		return
	}
	_, _ = fmt.Fprintf(Out, "lore version %s\n", version)
}

// VersionEnvelope is the stable JSON shape emitted by `lore --version --json`.
type VersionEnvelope struct {
	Version string `json:"version"`
}
