package main

import (
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

// newPathCmd builds the `lore path` cobra subcommand. Modes:
//
//   - `lore path <name>` prints the central-side path of that project's
//     agent directory. If the project has multiple, errors with a hint
//     to pass --dir.
//   - `lore path <name> --dir <agent-dir>` prints the central-side path
//     of the specific tracked agent dir.
//   - `lore path --repo` prints the central repo path itself.
func newPathCmd() *cobra.Command {
	var repoFlag bool
	var dirFlag string
	cmd := &cobra.Command{
		Use:   "path [name]",
		Short: "Print the central-repo path of a tracked project (or the repo itself)",
		Long: `Prints absolute paths suitable for shell scripting: ` +
			"`cd \"$(lore path foo)\"`" +
			`. Output is unstyled so ANSI codes never leak into a subshell.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if repoFlag {
				if len(args) > 0 {
					// usage errors print the cobra usage block so the
					// user sees how the command is meant to be called.
					// root has SilenceUsage: true, so cobra won't do this
					// automatically; we opt in here for the usage-error path.
					_ = cmd.Usage()
					return lerr.New(lerr.CodeGenericError,
						"lore path: --repo takes no arguments")
				}
				if dirFlag != "" {
					_ = cmd.Usage()
					return lerr.New(lerr.CodeGenericError,
						"lore path: --repo and --dir are mutually exclusive")
				}
				p, err := project.PathRepo()
				if err != nil {
					return err
				}
				output.PrintPath(p)
				return nil
			}
			if len(args) != 1 {
				_ = cmd.Usage()
				return lerr.New(lerr.CodeGenericError,
					"lore path requires a project name (or --repo)")
			}
			p, err := project.Path(args[0], dirFlag)
			if err != nil {
				return err
			}
			output.PrintPath(p)
			return nil
		},
	}
	cmd.Flags().BoolVar(&repoFlag, "repo", false, "Print the central repository path")
	cmd.Flags().StringVar(&dirFlag, "dir", "",
		"agent dir to print (required when the project has multiple)")
	return cmd
}
