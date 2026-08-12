package main

import (
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List tracked projects and checkout sync state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := project.List()
			if err != nil {
				return err
			}
			rows := make([]output.ListRow, 0, len(result.Rows))
			for _, row := range result.Rows {
				rows = append(rows, output.ListRow{Project: row.Name, Checkout: row.Path, AgentDirs: row.AgentDirs, SyncState: row.State, Issues: issuesForOutput(row.Issues)})
			}
			output.PrintList(output.ListResult{CentralRepo: result.CentralRepo, CentralHead: result.CentralHead, Rows: rows})
			return nil
		},
	}
}
