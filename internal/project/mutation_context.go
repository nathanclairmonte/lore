package project

import (
	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	"github.com/nathanclairmonte/lore/internal/state"
)

type mutationContext struct {
	config  config.Config
	central *central.Central
	state   state.State
	unlock  func() error
}

// openMutationContext establishes the shared central mutation ordering.
func openMutationContext() (*mutationContext, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return nil, err
	}
	c, err := openCentral(cfg.CentralRepo)
	if err != nil {
		return nil, err
	}
	unlock, err := c.LockMutations()
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = unlock()
		}
	}()
	if err := c.RecoverReplacements(); err != nil {
		return nil, err
	}
	st, _, err := state.Load()
	if err != nil {
		return nil, err
	}
	success = true
	return &mutationContext{config: cfg, central: c, state: st, unlock: unlock}, nil
}

func (c *mutationContext) close() {
	_ = c.unlock()
}
