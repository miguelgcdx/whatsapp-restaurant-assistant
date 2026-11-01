package cart

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const unresolvedEditAction = "mark_unresolved_edit"

type unresolvedEditRequest struct {
	Action   string             `json:"action"`
	Cart     string             `json:"cart"`
	Expected int64              `json:"expected"`
	Kind     UnresolvedEditKind `json:"kind"`
}

// resolutionBinding is canonical server provenance, never decoded model input.
// Cart/version are supplied by the enclosing concrete request. ViewDigest binds
// the complete original view, including exact request text and scoped sources.
type resolutionBinding struct {
	MarkerEvent     string `json:"marker_event"`
	MarkerSlot      int64  `json:"marker_slot"`
	MarkerOperation string `json:"marker_operation"`
	ViewSource      string `json:"view_source"`
	ViewControl     int64  `json:"view_control"`
	ViewDigest      string `json:"view_digest"`
}

type resolvedEditRequest struct {
	Action     string            `json:"action"`
	Edit       request           `json:"edit"`
	Resolution resolutionBinding `json:"resolution"`
}

func validUnresolvedKind(kind UnresolvedEditKind) bool {
	return kind == UnresolvedAdd || kind == UnresolvedRemove || kind == UnresolvedSetNote
}

// UnresolvedEditCall constructs an ambiguity descriptor, not a concrete Action.
// RegisterExecution can retain its identity but cannot persist an ACTIVE marker.
func UnresolvedEditCall(id CartID, expected Version, kind UnresolvedEditKind) (Call, error) {
	if !validID(id.value) || expected.value < 0 || !validUnresolvedKind(kind) {
		return Call{}, ErrInput
	}
	return Call{req: request{Action: unresolvedEditAction, Cart: id.value, Expected: expected.value}, unresolved: &unresolvedEditRequest{unresolvedEditAction, id.value, expected.value, kind}}, nil
}

func (b resolutionBinding) valid() bool {
	digest, err := hex.DecodeString(b.ViewDigest)
	return validID(b.MarkerEvent) && b.MarkerSlot >= 0 && validID(b.MarkerOperation) && validID(b.ViewSource) && b.ViewControl >= 0 && err == nil && len(digest) == sha256.Size && hex.EncodeToString(digest) == b.ViewDigest && b.ViewDigest != hex.EncodeToString(make([]byte, sha256.Size))
}

func encodeAmbiguousEditCall(call Call) ([]byte, error) {
	if call.unresolved != nil {
		return json.Marshal(call.unresolved)
	}
	return json.Marshal(resolvedEditRequest{call.req.Action, call.req, *call.resolution})
}

func decodeAmbiguousEditCall(body string, mark bool) (Call, error) {
	var call Call
	if mark {
		var r unresolvedEditRequest
		if json.Unmarshal([]byte(body), &r) != nil || r.Action != unresolvedEditAction {
			return Call{}, ErrStoredData
		}
		id, err := ParseCartID(r.Cart)
		if err != nil {
			return Call{}, ErrStoredData
		}
		v, err := ParseVersion(r.Expected)
		if err != nil {
			return Call{}, ErrStoredData
		}
		call, err = UnresolvedEditCall(id, v, r.Kind)
		if err != nil {
			return Call{}, ErrStoredData
		}
	} else {
		var r resolvedEditRequest
		if json.Unmarshal([]byte(body), &r) != nil || !r.Resolution.valid() || !validUnresolvedKind(UnresolvedEditKind(r.Action)) || r.Action != r.Edit.Action {
			return Call{}, ErrStoredData
		}
		// Reuse the concrete codec/constructors. A nested wrapper cannot bypass
		// concrete validation, gain extra targets or invent a different action.
		b, err := json.Marshal(r.Edit)
		if err != nil {
			return Call{}, ErrStoredData
		}
		call, err = decodeCall(string(b))
		if err != nil || call.action == nil {
			return Call{}, ErrStoredData
		}
		call.resolution = &r.Resolution
	}
	encoded, err := encodeAmbiguousEditCall(call)
	if err != nil || string(encoded) != body {
		return Call{}, ErrStoredData
	}
	return call, nil
}
