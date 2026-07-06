//go:build !windows

package main

// restrictSecretsDirWindows is a no-op on non-Windows platforms:
// the file store's Unix 0700/0600 permissions already restrict the secrets
// dir there (shnsdk.WriteBundle); the Windows ACL hardening in
// secretsacl_windows.go is additive and Windows-only.
func restrictSecretsDirWindows(path string) error { return nil }
