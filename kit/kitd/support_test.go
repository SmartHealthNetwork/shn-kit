package kitd

import (
	"strings"
	"testing"
)

func TestValidatorLoadsLocalValidationSupport(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		t.Run(line, func(t *testing.T) {
			spec, err := BuildValidatorChildSpec("/assets with spaces", "/jre", t.TempDir(), 18080, "linux", line)
			if err != nil {
				t.Fatal(err)
			}
			cfg := springConfig(t, spec.Env)
			prefix := "hapi.fhir.implementationguides.validationsupport."
			if cfg[prefix+"name"] != "shn.fhir.validation-support" || cfg[prefix+"version"] != "1.2.0" || !strings.HasSuffix(cfg[prefix+"packageUrl"], "/assets%20with%20spaces/igs-validator/shn.fhir.validation-support-1.2.0.tgz") {
				t.Fatalf("missing local support configuration: %v", cfg)
			}
		})
	}
}
