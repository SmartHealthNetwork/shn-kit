package runner

import (
	"encoding/json"
	"fmt"
	"net/http"

	scenariodriver "github.com/SmartHealthNetwork/shn-gateway/scenariodriver"

	"github.com/SmartHealthNetwork/shn-kit/event"
)

// submitAmendment sends a PAS amendment the runner built as the requester (the
// EHR on the Da Vinci lane) through the gateway's ingress, and sends it once
// more when the payer answers the first attempt 409 Conflict.
//
// A 409 (Conflict) is the payer refusing the amendment as it stands; any 409 is
// sent once more, whatever its reason. From the hosted reference payer it is a
// version conflict: its store refused the amendment because the record it
// updates moved in the meantime, its pend-resolution timer writing the same
// ClaimResponse in the same instant. The gateway relays that 409 as the payer sent it
// and never re-sends a requester's amendment: whether to send again is the
// requester's call. This is the runner making that call the way a gateway that
// builds an amendment for its own participant does (shn-gateway v0.56.0): one
// more attempt, under a new correlation id, rebuilt by build with nothing else
// changed; the answer to that attempt is the one the row reads, and a second
// 409 fails the row with the payer's own status and body.
//
// Only a 409 is sent again. Any other status is the row's error after one
// attempt, and a transport error is never retried: the runner cannot tell
// whether the payer received that attempt.
//
// corr is the first attempt's correlation id; prefix is what the re-send's id
// is minted from, the same way the first was (randCorr). The re-send is
// reported on the run timeline (event.TypeRunResent) naming both ids, which is
// how the Inspector links the second attempt to the refused one.
//
// It returns the bundle whose answer it returns, and that answer (status 200).
func (rn *Runner) submitAmendment(uc, prefix, corr string, build func(corr string) ([]byte, error)) ([]byte, scenariodriver.PASOutcome, error) {
	bundle, err := build(corr)
	if err != nil {
		return nil, scenariodriver.PASOutcome{}, fmt.Errorf("runner: conformant/%s: %w", uc, err)
	}
	out, err := rn.cfg.Driver.SubmitPAS(bundle)
	if err != nil {
		return nil, scenariodriver.PASOutcome{}, fmt.Errorf("runner: conformant/%s: submit amended re-POST: %w", uc, err)
	}
	if out.Status == http.StatusConflict {
		refused := corr
		corr = randCorr(prefix)
		if bundle, err = build(corr); err != nil {
			return nil, scenariodriver.PASOutcome{}, fmt.Errorf("runner: conformant/%s: %w", uc, err)
		}
		rn.reportResent(refused, corr, out.Status)
		if out, err = rn.cfg.Driver.SubmitPAS(bundle); err != nil {
			return nil, scenariodriver.PASOutcome{}, fmt.Errorf("runner: conformant/%s: submit amended re-POST, sent once more after the payer's 409: %w", uc, err)
		}
		if out.Status != http.StatusOK {
			return nil, scenariodriver.PASOutcome{}, conformantIngressErr(uc+": amend, sent once more after the payer's 409,", out.Status, out.Body)
		}
	}
	if out.Status != http.StatusOK {
		return nil, scenariodriver.PASOutcome{}, conformantIngressErr(uc+": amend", out.Status, out.Body)
	}
	return bundle, out, nil
}

// resentDetail is event.TypeRunResent's Detail.
type resentDetail struct {
	CorrelationID        string `json:"correlationId"`
	RefusedCorrelationID string `json:"refusedCorrelationId"`
	Status               int    `json:"status"`
}

// reportResent puts the re-send on the run timeline, stamped with the run
// currently holding the runner.
func (rn *Runner) reportResent(refused, corr string, status int) {
	if rn.cfg.Bus == nil {
		return
	}
	detail, _ := json.Marshal(resentDetail{CorrelationID: corr, RefusedCorrelationID: refused, Status: status})
	rn.cfg.Bus.Emit(event.Event{Type: event.TypeRunResent, RunID: rn.stamp.RunID, Lane: rn.stamp.Lane, UC: rn.stamp.UC, Detail: string(detail)})
}
