package main

import (
	"fmt"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/prompt"
	"github.com/spf13/cobra"
)

// newInitCmd builds the `lore init` cobra subcommand. Slice 1 ships only
// the no-flag form; --path, --from, --remote, --no-interactive arrive in
// later slices.
func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the central repository and write a default config",
		Long: "Initializes lore on this machine. Creates the central " +
			"agent-context repository (default ~/agent-context) as a git " +
			"repo and writes ~/.config/lore/config.toml with sensible " +
			"defaults.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit()
		},
	}
}

// runInit dispatches across the five behaviour branches defined in the
// slice-1 plan. Returns a lore *errors.Error on refuse paths; main.go
// translates that to the right exit code.
func runInit() error {
	cfg, configExists, err := config.Load()
	if err != nil {
		return err
	}
	dirExists, err := central.Exists(cfg.CentralRepo)
	if err != nil {
		return err
	}

	switch {
	case configExists && dirExists:
		return lerr.Newf(
			lerr.CodeConflict,
			"central repo already configured at %s",
			config.Contract(cfg.CentralRepo),
		).WithHint(
			"Use `lore init --path <new-path>` to switch, " +
				"`lore use <existing-path>` to point lore at an existing repo, " +
				"or remove the directory first.",
		)

	case configExists && !dirExists:
		// configured but the central dir is gone. Offer to re-create.
		return reinitMissingDir(cfg.CentralRepo)

	case !configExists && dirExists:
		// no config yet, but something already sits at the default path.
		empty, err := central.IsEmpty(cfg.CentralRepo)
		if err != nil {
			return err
		}
		if !empty {
			return lerr.Newf(
				lerr.CodeRefusedSafety,
				"%s already exists and is not empty",
				config.Contract(cfg.CentralRepo),
			).WithHint(
				"Remove it first, or use `lore init --path <other-path>` " +
					"(coming soon) to point lore at a different location.",
			)
		}
		// empty existing dir is fine — fall through to fresh init.
		return freshInit(cfg.CentralRepo, false)

	default:
		// no config, no dir — clean first-run.
		return freshInit(cfg.CentralRepo, false)
	}
}

// freshInit performs the central.Initialize + config.WriteDefaults + print
// success path. The recreated flag controls only the verb in the success
// message.
//
// Order matters: initialize the central repo first, THEN write the config.
// If init fails partway through the user can re-run safely; if config
// existed first and central init failed, we'd have an orphan config
// pointing at a half-built repo.
func freshInit(path string, recreated bool) error {
	c, err := central.Initialize(path)
	if err != nil {
		return err
	}

	// only write config on fresh init; recreate path is "config already
	// exists, dir was missing" so leave config alone.
	if !recreated {
		if err := config.WriteDefaults(); err != nil {
			return err
		}
	}

	hasRemote, err := c.HasRemote()
	if err != nil {
		return err
	}
	configPath, err := config.Path()
	if err != nil {
		return err
	}

	output.PrintInitResult(output.InitResult{
		CentralPath: c.Path,
		ConfigPath:  configPath,
		HasRemote:   hasRemote,
		Recreated:   recreated,
	})
	return nil
}

// reinitMissingDir handles the "config points at <path> but <path> is
// gone" case. Asks the user before re-creating; refuses with exit 7 on
// either an explicit "no" or non-interactive stdin.
func reinitMissingDir(path string) error {
	question := fmt.Sprintf(
		"Configured central repo at %s is missing. Re-create it?",
		config.Contract(path),
	)
	yes, err := prompt.Confirm(question, false)
	if err != nil {
		// non-TTY case bubbles up as CodeInconsistency from prompt.
		return err
	}
	if !yes {
		return lerr.Newf(
			lerr.CodeInconsistency,
			"central repo is missing at %s",
			config.Contract(path),
		).WithHint(
			"Re-run `lore init` and accept the prompt, or recreate the " +
				"directory manually and re-run.",
		)
	}
	return freshInit(path, true)
}
