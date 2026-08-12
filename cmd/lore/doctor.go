package main

import (
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check project-copy and sync-base health",
		Long: `Check every recorded checkout for missing or invalid copies,
unreachable sync-bases, unresolved conflicts, and divergence.

Use --fix to restore missing agent directories from committed central
content. Doctor never resolves divergence or replaces an existing copy.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := project.Doctor(fix)
			if err == nil || len(result.Status.Projects) > 0 || len(result.Fixed) > 0 {
				output.PrintDoctor(output.DoctorResult{Healthy: result.Healthy, Fixed: result.Fixed, Status: syncResultForOutput(result.Status)})
			}
			if err != nil {
				return err
			}
			if !result.Healthy {
				return lerr.New(lerr.CodeInconsistency, "doctor found unhealthy checkouts").WithHint("run `lore status` for the complete sync report")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "restore missing project-copy directories when safe")
	return cmd
}
