package output

import (
	"fmt"

	"github.com/nathanclairmonte/lore/internal/config"
)

// InitResult is the data lore init hands to the output layer.
type InitResult struct {
	CentralPath string // absolute path to the central repo
	ConfigPath  string // absolute path to the config file
	HasRemote   bool   // whether the central repo has a git remote configured
	Recreated   bool   // true if we re-created a missing dir (vs. fresh init)
}

// PrintInitResult prints the success summary plus the "no remote configured"
// hint when applicable. Paths are contracted back to `~/` for readability.
func PrintInitResult(r InitResult) {
	verb := "Initialized"
	if r.Recreated {
		verb = "Re-created"
	}
	Success("%s central repository at %s", verb, config.Contract(r.CentralPath))
	Success("Wrote config to %s", config.Contract(r.ConfigPath))

	if !r.HasRemote {
		Blank()
		Warn("No remote configured. Set one with:")
		// print the contracted central path literally rather than
		// `$(lore path --repo)` — `lore path` arrives in slice 2; until
		// then the hint should be a working command the user can run.
		fmt.Fprintf(Out, "    cd %s && git remote add origin <url>\n", config.Contract(r.CentralPath))
	}
}
