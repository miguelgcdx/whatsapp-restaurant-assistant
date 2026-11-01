package cart

import (
	"encoding/hex"
	"encoding/json"
)

const (
	pickupPrepareAction = "prepare_pickup_summary"
	pickupSubmitAction  = "submit_pickup_order"
)

// New codec bytes deliberately do not extend any legacy/draft/preview body.
// References bind parameters; none selects source scope or supplies authority.
type pickupRequest struct {
	Codec             string                   `json:"codec"`
	Action            string                   `json:"action"`
	Cart              string                   `json:"cart,omitempty"`
	Expected          *int64                   `json:"expected,omitempty"`
	Candidate         string                   `json:"candidate_id,omitempty"`
	InboundEvent      string                   `json:"inbound_event_id,omitempty"`
	ContextDigest     string                   `json:"context_digest,omitempty"`
	AssessmentVersion string                   `json:"assessment_version,omitempty"`
	Decision          PickupCompletionDecision `json:"decision,omitempty"`
}

// PreparePickupCall creates a closed parameter descriptor, never authority.
// The trusted orchestrator explicitly registers it with its persisted event and
// nonnegative runtime slot, then supplies the opaque Execution to PickupRuntime.
func PreparePickupCall(id CartID, expected Version) (Call, error) {
	if !validID(id.value) || expected.value < 0 {
		return Call{}, ErrInput
	}
	v := expected.value
	return Call{req: request{Action: pickupPrepareAction, Cart: id.value, Expected: v}, pickup: &pickupRequest{Codec: "local-pickup-v1", Action: pickupPrepareAction, Cart: id.value, Expected: &v}}, nil
}

// SubmitPickupCall binds trusted assessment parameters, never customer identity.
// InboundEventID is the canonical stored event UUID (Source.EventUUID), not an
// arbitrary provider reference. Backend execution must independently validate
// the registered descriptor, actual claim, source/context and stored evidence.
func SubmitPickupCall(e PickupCompletionEvidence) (Call, error) {
	if !validID(e.CandidateID.value) || !validID(e.InboundEventID) || e.ContextDigest == [32]byte{} || !text(e.AssessmentVersion, 128, true) || (e.Decision != PickupComplete && e.Decision != PickupNotComplete) {
		return Call{}, ErrInput
	}
	return Call{req: request{Action: pickupSubmitAction}, pickup: &pickupRequest{Codec: "local-pickup-v1", Action: pickupSubmitAction, Candidate: e.CandidateID.value, InboundEvent: e.InboundEventID, ContextDigest: hex.EncodeToString(e.ContextDigest[:]), AssessmentVersion: e.AssessmentVersion, Decision: e.Decision}}, nil
}

func decodePickupCall(body string) (Call, error) {
	var w pickupRequest
	if len(body) == 0 || len(body) > resultBudget || json.Unmarshal([]byte(body), &w) != nil || w.Codec != "local-pickup-v1" {
		return Call{}, ErrStoredData
	}
	var call Call
	var err error
	switch w.Action {
	case pickupPrepareAction:
		id, e := ParseCartID(w.Cart)
		if e != nil || w.Expected == nil {
			return Call{}, ErrStoredData
		}
		v, e := ParseVersion(*w.Expected)
		if e != nil {
			return Call{}, ErrStoredData
		}
		call, err = PreparePickupCall(id, v)
	case pickupSubmitAction:
		var id PickupCandidateID
		if id.UnmarshalText([]byte(w.Candidate)) != nil {
			return Call{}, ErrStoredData
		}
		digest, e := hex.DecodeString(w.ContextDigest)
		if e != nil || len(digest) != 32 {
			return Call{}, ErrStoredData
		}
		var hash [32]byte
		copy(hash[:], digest)
		call, err = SubmitPickupCall(PickupCompletionEvidence{CandidateID: id, InboundEventID: w.InboundEvent, ContextDigest: hash, AssessmentVersion: w.AssessmentVersion, Decision: w.Decision})
	default:
		return Call{}, ErrStoredData
	}
	if err != nil {
		return Call{}, ErrStoredData
	}
	canonical, err := encodeCall(call)
	if err != nil || string(canonical) != body {
		return Call{}, ErrStoredData
	}
	return call, nil
}
