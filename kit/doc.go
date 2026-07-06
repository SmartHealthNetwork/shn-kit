// Package kit is the SHN Kit's Go daemon module (shnkitd): child-process
// supervision, the loopback session-token-gated API, the observer event
// relay, and scenario driving for the desktop participant kit.
//
// Boundary fence: this module imports ONLY the public SDK
// (shn-sdk), the public gateway module (shn-gateway), and stdlib — never
// the private substrate's internal packages. kit/boundary_test.go enforces it.
package kit
