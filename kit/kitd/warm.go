package kitd

import (
	"context"
	"fmt"
	"net/http"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// WarmDeadline bounds the seeder's first $validate against the Kit's data
// server. A cold HAPI pays its validation-support initialisation on the first
// $validate after boot, and on a small host that first call has been measured
// near the 30 s client budget every consumer gives a $validate — so the seeder
// pays it here, before the seed-complete marker the Kit's smokes wait on, under
// a deadline of its own.
//
// This is the Kit's twin of the gateway seed loader's warm-up (the gateway
// module the Kit pins does not carry it yet); test/validatorhealth fences the
// two deadlines against each other.
const WarmDeadline = 300 * time.Second

const warmBody = `{"resourceType":"Patient","id":"seed-warm","meta":{"profile":["http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient"]},"identifier":[{"system":"urn:shn:seed","value":"warm"}],"name":[{"family":"Warm","given":["Seed"]}],"gender":"unknown"}`

// WarmValidate posts one $validate to {base}/{tenant} through a client whose
// timeout is WarmDeadline — never a consumer's client — and returns how long the
// server took; an answer past the deadline is an error naming it.
func WarmValidate(ctx context.Context, base, tenant string, logf func(string, ...any)) (time.Duration, error) {
	return warmValidate(ctx, base, tenant, WarmDeadline, logf)
}

func warmValidate(ctx context.Context, base, tenant string, deadline time.Duration, logf func(string, ...any)) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	v := shnsdk.NewOperationValidator(base + "/" + tenant)
	v.Client = &http.Client{Timeout: deadline}
	start := time.Now()
	if _, err := v.Validate(ctx, []byte(warmBody), ""); err != nil {
		if ctx.Err() != nil {
			return time.Since(start), fmt.Errorf("validator warm-up did not answer within %s: %w", deadline, err)
		}
		return time.Since(start), fmt.Errorf("validator warm-up: %w", err)
	}
	elapsed := time.Since(start)
	if logf != nil {
		logf("kitd: validator warm in %.1fs (%s/%s)", elapsed.Seconds(), base, tenant)
	}
	return elapsed, nil
}
