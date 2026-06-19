package main

import "errors"

// runService boots the long-running service (scheduler + web dashboard).
//
// This is a placeholder wired up in the internal/schedule + internal/web step
// of the build order. Keeping the signature stable lets the CLI and the rest of
// the scaffold compile while those modules are built.
func runService(args []string) error {
	_ = args
	return errors.New("service not yet implemented (wired up in the scheduler/web build step)")
}
