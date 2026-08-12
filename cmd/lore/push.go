package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/spf13/cobra"
)

const pushAutoFlagName = "auto"

// newPushCmd builds the project-copy to central-copy command.
func newPushCmd() *cobra.Command {
	var opts project.PushOptions
	var auto bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Capture project-copy changes in central",
		Long: `Copy local agent-directory changes into central and commit them.

Push is fast-forward-only. It refuses central-ahead or diverged
checkouts and tells you to pull first. It never contacts a Git remote.
Use plain Git in the central repository for remote pushes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := project.Push(opts)
			if err != nil {
				if auto {
					recordPushFailure(cmd.CommandPath(), pushFailureScope(opts, result), err)
				}
				return err
			}
			if auto {
				clearPushFailure()
			}
			output.PrintPush(pushResultForOutput(result), auto)
			return nil
		},
	}
	cmd.Flags().BoolVar(&auto, pushAutoFlagName, false, "suppress output when there is nothing to push")
	cmd.Flags().StringVar(&opts.Project, "project", "", "push one tracked project")
	cmd.Flags().BoolVar(&opts.ProjectFromCWD, "project-from-cwd", false, "push the tracked checkout containing cwd")
	return cmd
}

func pushResultForOutput(result project.PushResult) output.PushResult {
	out := output.PushResult{CentralRepo: result.CentralRepo, PreviousHead: result.PreviousHead, CommitSHA: result.CommitSHA, CommitMessage: result.CommitMessage, Projects: []output.PushProjectResult{}}
	for _, projectResult := range result.Projects {
		projectOut := output.PushProjectResult{Name: projectResult.Name, Checkouts: []output.PushCheckoutResult{}}
		for _, checkout := range projectResult.Checkouts {
			projectOut.Checkouts = append(projectOut.Checkouts, output.PushCheckoutResult{Path: checkout.Path, Status: output.PushStatus(checkout.Status)})
		}
		out.Projects = append(out.Projects, projectOut)
	}
	return out
}

func pushFailurePath() (string, error) {
	return state.SyncErrorPath()
}

func pushFailureScope(opts project.PushOptions, result project.PushResult) string {
	if opts.Project != "" {
		return opts.Project
	}
	if result.ScopeProject != "" {
		return result.ScopeProject
	}
	if opts.ProjectFromCWD {
		return "cwd"
	}
	return "all"
}

func recordPushFailure(command, scope string, err error) {
	path, pathErr := pushFailurePath()
	if pathErr != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	content := fmt.Sprintf("timestamp: %s\ncommand: %s\nscope: %s\nexit_code: %d\nerror: %s\n", time.Now().Format(time.RFC3339), command, scope, lerr.CodeOf(err), err)
	_ = os.WriteFile(path, []byte(content), 0o644)
}

func clearPushFailure() {
	path, err := pushFailurePath()
	if err == nil {
		_ = os.Remove(path)
	}
}

func surfacePendingPushFailure(cmd *cobra.Command, pushCommand *cobra.Command) {
	if cmd == pushCommand {
		if auto, err := cmd.Flags().GetBool(pushAutoFlagName); err == nil && auto {
			return
		}
	}
	if output.GetMode() == output.ModeJSON {
		return
	}
	path, err := pushFailurePath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if err := output.PrintDeferredFailure(data); err == nil {
		_ = os.Remove(path)
	}
}
