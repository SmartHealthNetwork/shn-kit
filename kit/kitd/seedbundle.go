// seedbundle.go — GET /api/byo/seed-bundle/{lane}: the two synthetic seed
// bundles (frozen bytes shipped by the shn-gateway module, not derived from
// this install's BYO swap state) offered for download so a partner can POST
// them to their own FHIR server. The Kit NEVER writes to a partner's
// connected FHIR server itself — this endpoint is download-only.
package kitd

import (
	"net/http"

	"github.com/SmartHealthNetwork/shn-gateway/fhirseed"
)

// handleBYOSeedBundleGet serves GET /api/byo/seed-bundle/{lane}: lane
// "conformant" is the frozen conformant-lane Patient roster
// (fhirseed.ConformantSeedBundle, 5 members); lane "ehr" is the provider-data
// persona set with every Observation's effectiveDateTime freshened to now
// (fhirseed.FreshenObservations(fhirseed.ProviderDataSeedBundle()) — the same
// freshening FreshenPersonas applies at boot, so the downloaded bundle stays
// inside the operated CQL's ObservationLookBack window). Deliberately NOT
// gated on d.cfg.BYO == nil, unlike the rest of /api/byo/*: this serves
// frozen bytes regardless of BYO swap state, exactly like handleSupportBundle.
func (d *Daemon) handleBYOSeedBundleGet(w http.ResponseWriter, r *http.Request) {
	lane := r.PathValue("lane")

	var body []byte
	switch lane {
	case "conformant":
		body = fhirseed.ConformantSeedBundle()
	case "ehr":
		freshened, err := fhirseed.FreshenObservations(fhirseed.ProviderDataSeedBundle())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		body = freshened
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown lane: " + lane})
		return
	}

	w.Header().Set("Content-Type", "application/fhir+json")
	w.Header().Set("Content-Disposition", `attachment; filename="shn-`+lane+`-personas.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
