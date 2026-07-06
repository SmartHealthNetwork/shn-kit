//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"os/user"
)

// restrictSecretsDirWindows applies a best-effort restrictive DACL to the
// secrets bundle dir on Windows: the bundle holds this Kit's
// signing keypair, and Windows' default directory ACLs are broader than the
// Unix 0700/0600 posture shnsdk.WriteBundle already achieves on macOS/Linux.
// Uses icacls (no cgo, matching the go-keyring dependency's own posture):
// "/inheritance:r" breaks inherited ACEs, "/grant:r <user>:(OI)(CI)F" grants
// the CURRENT user full control recursively, REPLACING (":r") anything
// icacls itself previously granted — so calling this again at every boot is
// idempotent rather than accumulating ACEs. Best-effort: a failure here
// never blocks boot or provisioning — the caller logs it and folds it into
// the bootstrap probe Detail (fail-visible, never silent).
func restrictSecretsDirWindows(path string) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("secrets dir ACL: resolve current user: %w", err)
	}
	out, err := exec.Command("icacls", path, "/inheritance:r", "/grant:r", u.Username+":(OI)(CI)F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("secrets dir ACL: icacls: %w: %s", err, out)
	}
	return nil
}
