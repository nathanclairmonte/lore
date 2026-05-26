package main

import (
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

// newPathCmd builds the `lore path` cobra subcommand. Two modes:
//
//   - `lore path <name>` prints the central-side path of that project's
//     (single, in slice 2) agent directory.
//   - `lore path --repo` prints the central repo path itself.
//
// Slice 5 will add `--dir <name>` for projects with multiple agent dirs.
func newPathCmd() *cobra.Command {
	var repoFlag bool
	cmd := &cobra.Command{
		Use:   "path [name]",
		Short: "Print the central-repo path of a tracked project (or the repo itself)",
		Long: "Prints absolute paths suitable for shell scripting: `cd " +
			"\"$(lore path foo)\"`. Output is unstyled so ANSI codes never " +
			"leak into a subshell.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if repoFlag {
				if len(args) > 0 {
					return lerr.New(lerr.CodeGenericError,
						"lore path: --repo takes no arguments")
				}
				p, err := project.PathRepo()
				if err != nil {
					return err
				}
				output.PrintPath(p)
				return nil
			}
			if len(args) != 1 {
				return lerr.New(lerr.CodeGenericError,
					"lore path requires a project name (or --repo)")
			}
			p, err := project.Path(args[0])
			if err != nil {
				return err
			}
			output.PrintPath(p)
			return nil
		},
	}
	cmd.Flags().BoolVar(&repoFlag, "repo", false, "Print the central repository path")
	return cmd
}
