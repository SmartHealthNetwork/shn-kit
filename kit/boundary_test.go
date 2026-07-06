package kit

import (
	"os/exec"
	"strings"
	"testing"
)

// TestKitBoundaryFence asserts the shn-kit module's FULL closure — production
// imports, in-package test imports (TestImports), AND external `_test`
// package imports (XTestImports) — references no private substrate-internal
// package at all. This is the Kit's publish boundary: kit code may import only
// shn-sdk, shn-gateway, and stdlib. The gate that needs substrate internals
// lives in the private substrate repo, never here.
func TestKitBoundaryFence(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps=false",
		"-f", "{{.ImportPath}}|{{range .Imports}}{{.}} {{end}}{{range .TestImports}}{{.}} {{end}}{{range .XTestImports}}{{.}} {{end}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		pkg, imports := parts[0], parts[1]
		for _, imp := range strings.Fields(imports) {
			if strings.Contains(imp, "SmartHealthNetwork/shn-platform/") {
				t.Errorf("%s imports forbidden monorepo package %s — kit code may import only shn-sdk, shn-gateway, and stdlib", pkg, imp)
			}
		}
	}
}
