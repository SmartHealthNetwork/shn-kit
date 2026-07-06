// Command prewarm drives the package-time persona seed against a locally booted
// data-server WAR (see tools/kitassets/build.sh). This is build tooling only: it
// runs once on the asset-pipeline host while a packaged data server is booted for
// seeding, and never runs on a user machine. Every artifact it installs goes
// through the exported gateway/fhirseed API — including the operated-$populate
// prepop CQL Library the sandbox DTR questionnaire depends on, built by
// fhirseed.SandboxLumbarLibrary and installed with fhirseed.PutGlobalArtifact.
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/SmartHealthNetwork/shn-gateway/fhirseed"
	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

func main() {
	base := flag.String("base", "", "untenanted FHIR base URL, e.g. http://127.0.0.1:18081/fhir")
	flag.Parse()
	if *base == "" {
		log.Fatal("prewarm: --base required (untenanted FHIR base)")
	}
	ctx := context.Background()
	c := &fhirseed.Client{Base: *base, Logf: log.Printf}
	step := func(name string, err error) {
		if err != nil {
			log.Fatalf("prewarm: %s: %v", name, err)
		}
		log.Printf("prewarm: %s OK", name)
	}
	step("WaitReady", c.WaitReady(ctx, 20*time.Minute))
	step("CreatePartitions(provider)", c.CreatePartitions(ctx, []string{"provider"}))
	step("InstallCRLibraries", c.InstallCRLibraries(ctx))

	// Install the operated-CQL LumbarMRICQL prepop Library into the DEFAULT
	// partition — a Library is a non-partitionable knowledge artifact, so it
	// cannot live inside a tenant partition. Without this the sandbox lumbar
	// questionnaire's $populate fails "Could not load source for library
	// LumbarMRICQL" and every DTR-carrying scenario denies at 0 weeks.
	lumbarLib, err := fhirseed.SandboxLumbarLibrary()
	if err != nil {
		log.Fatalf("prewarm: SandboxLumbarLibrary: %v", err)
	}
	dbase := *base + "/DEFAULT"
	v := shnsdk.NewOperationValidator(dbase)
	step("InstallLumbarLibrary", fhirseed.PutGlobalArtifact(ctx, dbase, v, "Library", "LumbarMRICQL", lumbarLib))

	step("WarmUpPopulate", c.WarmUpPopulate(ctx))
	step("LoadProviderDataBundles(provider)", c.LoadProviderDataBundles(ctx, "provider"))

	// The sandbox personas bundle carries baked static effectiveDateTime values
	// that age out of the CQL engine's 3-month observation lookback window.
	// Freshen them the same way LoadProviderDataBundles freshens the provider
	// data bundles, so the packaged Kit's therapy-weeks answer is correct from
	// day one instead of silently rotting a few months after packaging.
	personasBundle, err := fhirseed.FreshenObservations(fhirseed.SandboxProviderPersonasBundle())
	if err != nil {
		log.Fatalf("prewarm: FreshenObservations(personas): %v", err)
	}
	step("PostTransaction(personas)", c.PostTransaction(ctx, "provider", personasBundle))
	step("WriteSeedMarker(provider)", c.WriteSeedMarker(ctx, "provider"))
	log.Print("prewarm: seed complete")
}
