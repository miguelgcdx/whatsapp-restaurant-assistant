package cart

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
)

func pickupCodecEvidence(t *testing.T) PickupCompletionEvidence {
	t.Helper()
	var id PickupCandidateID
	if err := id.UnmarshalText([]byte("20000000-0000-4000-8000-000000000001")); err != nil {
		t.Fatal(err)
	}
	return PickupCompletionEvidence{CandidateID: id, InboundEventID: "20000000-0000-4000-8000-000000000002", ContextDigest: sha256.Sum256([]byte("synthetic persisted context")), AssessmentVersion: "local-pickup-assessment-v1", Decision: PickupComplete}
}

func TestPickupCodecPrepareParameters(t *testing.T) {
	id, _ := ParseCartID("20000000-0000-4000-8000-000000000003")
	for _, version := range []int64{0, 7} {
		v, _ := ParseVersion(version)
		call, err := PreparePickupCall(id, v)
		if err != nil {
			t.Fatalf("valid cart/version %d rejected: %v", version, err)
		}
		body, err := encodeCall(call)
		if err != nil || !strings.Contains(string(body), `"codec":"local-pickup-v1","action":"prepare_pickup_summary"`) {
			t.Fatalf("missing distinct prepare descriptor: %s %v", body, err)
		}
	}
	for _, invalid := range []struct {
		id CartID
		v  Version
	}{{CartID{}, Version{}}, {id, Version{value: -1}}} {
		if _, err := PreparePickupCall(invalid.id, invalid.v); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid prepare accepted: %v", err)
		}
	}
}

func TestPickupCodecAssessmentParameters(t *testing.T) {
	evidence := pickupCodecEvidence(t)
	for _, decision := range []PickupCompletionDecision{PickupComplete, PickupNotComplete} {
		in := evidence
		in.Decision = decision
		if _, err := SubmitPickupCall(in); err != nil {
			t.Fatalf("valid assessment decision %q rejected: %v", decision, err)
		}
	}
	for _, mutate := range []func(*PickupCompletionEvidence){
		func(e *PickupCompletionEvidence) { e.CandidateID = PickupCandidateID{} },
		func(e *PickupCompletionEvidence) { e.InboundEventID = "caller supplied source" },
		func(e *PickupCompletionEvidence) { e.ContextDigest = [32]byte{} },
		func(e *PickupCompletionEvidence) { e.AssessmentVersion = "" },
		func(e *PickupCompletionEvidence) { e.AssessmentVersion = strings.Repeat("v", 129) },
		func(e *PickupCompletionEvidence) { e.AssessmentVersion = "v\x00" },
		func(e *PickupCompletionEvidence) { e.Decision = "confirmed" },
	} {
		in := evidence
		mutate(&in)
		if _, err := SubmitPickupCall(in); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid assessment accepted: %v", err)
		}
	}
}

func TestPickupCodecCanonicalReload(t *testing.T) {
	id, _ := ParseCartID("20000000-0000-4000-8000-000000000003")
	version, _ := ParseVersion(7)
	prepare, err := PreparePickupCall(id, version)
	if err != nil {
		t.Fatal(err)
	}
	submit, err := SubmitPickupCall(pickupCodecEvidence(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []Call{prepare, submit} {
		body, err := encodeCall(call)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeCall(string(body))
		if err != nil {
			t.Fatalf("canonical reload: %v", err)
		}
		again, err := encodeCall(decoded)
		if err != nil || string(again) != string(body) {
			t.Fatalf("changed intent on reload: %s %v", again, err)
		}
		for _, bad := range []string{
			string(body) + "\n",
			strings.TrimSuffix(string(body), "}") + `,"confirmed":true}`,
			strings.TrimSuffix(string(body), "}") + `,"action":"read"}`,
			strings.TrimSuffix(string(body), "}") + `,"restaurant_id":"forged"}`,
			strings.Replace(string(body), "local-pickup-v1", "local-preview-v1", 1),
		} {
			if _, err := decodeCall(bad); !errors.Is(err, ErrStoredData) {
				t.Fatalf("noncanonical/forged descriptor accepted: %q %v", bad, err)
			}
		}
	}
	for _, bad := range []string{
		`{"codec":"local-pickup-v1","action":"prepare_pickup_summary","cart":"20000000-0000-4000-8000-000000000003"}`,
		`{"codec":"local-pickup-v1","action":"prepare_pickup_summary","cart":"20000000-0000-4000-8000-000000000003","expected":null}`,
		`{"codec":"local-pickup-v1","action":"submit_pickup_order","confirmed":true}`,
	} {
		if _, err := decodeCall(bad); !errors.Is(err, ErrStoredData) {
			t.Fatalf("incomplete descriptor accepted: %q %v", bad, err)
		}
	}
}
