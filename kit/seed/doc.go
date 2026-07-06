// Package seed ships the SHN Kit's bring-your-own seed artifacts: FHIR
// transaction Bundles a bring-your-own operator manually POSTs to their OWN
// test FHIR server so the Kit's SEEDED scenario rows can resolve the
// members they hardcode there, once an EHR swap is applied ("the other lane
// keeps running seeded when the swap target carries the seeded members").
//
// demo-personas-conformant.json is the conformant lane's artifact: a
// Patient-only transaction Bundle (idempotent PUT entries) carrying the
// urn:shn:member identifier plus demographics for every member
// kit/runner/rows_conformant.go's conformant-lane rows hardcode
// (internal/fhirseed.ConformantLaneDemoMembers, in the root substrate
// module, is the source list). Regenerate it from the repo root with:
//
//	go run ./tools/kitconformantseedgen kit/seed/demo-personas-conformant.json
//
// This package itself carries no generator dependency — kit never imports
// the root module's internal packages; the
// generator and its drift guard (internal/fhirseed/bake_conformant_test.go)
// live in the root module, and only the resulting bytes are committed here.
//
// Synthetic data only: load this bundle into a TEST FHIR server you
// control, never a production system. Loading it is your own action on
// your own system (customer-operated-edge stewardship) — the Kit never
// writes to your connected EHR for you.
package seed
