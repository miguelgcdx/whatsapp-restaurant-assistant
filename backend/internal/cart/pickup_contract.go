package cart

import (
	"context"
	"errors"
	"time"

	"restaurant-assistant/backend/internal/foundation"
)

// This file declares the connected LOCAL pickup ports, not their implementation.
// LocalContext and parsed IDs alone remain insufficient submission authority.
// Synthetic presentation must never be represented as live-provider evidence.

type pickupCandidateKind struct{}
type pickupOrderKind struct{}
type pickupIntentKind struct{}
type pickupPresentationKind struct{}

type PickupCandidateID = ID[pickupCandidateKind]
type PickupOrderID = ID[pickupOrderKind]
type PickupIntentID = ID[pickupIntentKind]
type PickupPresentationID = ID[pickupPresentationKind]

// These limits apply to the exact assessment input, not order size or staff pages.
const (
	PickupCompletionAssessmentVersion = "pickup-completion-v1"
	PickupCompletionHistoryLimit      = 62
	PickupCompletionInputByteLimit    = 64 * 1024
)

var (
	ErrPickupConfirmationRequired = errors.New("pickup completion evidence required")
	ErrPickupSummaryRequired      = errors.New("current pickup summary required")
	ErrPickupInvalidTransition    = errors.New("invalid pickup order transition")
	ErrPickupPolicyUnresolved     = errors.New("pickup exception policy unresolved")
	ErrPickupForbidden            = errors.New("pickup staff access forbidden")
)

// PickupExecution is supplied by the trusted LOCAL orchestrator, never decoded
// from model arguments. Implementations must check persisted source, exact event,
// actual current claim, control epoch and scope, including after blocking reads.
// Prepare/Submit require Execution from RegisterExecution for the explicit
// source/runtime slot and matching descriptor; zero/mismatched handles grant no
// operation authority. CompletionContext is read-only: it checks Source, Claim
// and current control directly before an assessment/submit plan exists, without
// allocating an operation. No implicit or reserved runtime slot is selected.
type PickupExecution struct {
	Local          LocalContext
	Source         Source
	Execution      Execution
	Claim          foundation.Job
	ControlVersion Version
}

type PickupMinor int64

type PickupMoney struct {
	AmountMinor PickupMinor
	Currency    string
	Exponent    int16
}

// A nil estimate means unavailable, not zero or automatic readiness.
type PickupEstimate struct {
	Minutes     int64
	AsOf        time.Time
	Provisional bool
}

type PickupSummaryIntent struct {
	ID             PickupIntentID
	CandidateID    PickupCandidateID
	ConversationID ConversationID
	Text           string
	TextDigest     [32]byte
}

type PreparedPickupSummary struct {
	CandidateID     PickupCandidateID
	CartID          CartID
	CartVersion     Version
	UnitCount       int
	Total           PickupMoney
	Estimate        *PickupEstimate
	ClosingQuestion string
	Intent          PickupSummaryIntent
}

// PickupLocalPresentation is a trusted fake receiver callback AFTER it receives
// the stored intent's exact text. Neither enqueue nor provider acceptance counts.
// The server checks scope/linkage/digest and timestamps the stored receipt itself.
// This operation is not exposed as a model tool or a customer/staff HTTP route.
type PickupLocalPresentation struct {
	IntentID           PickupIntentID
	CandidateID        PickupCandidateID
	ReceiverReference  string
	ReceivedTextDigest [32]byte
}

type PickupPresentation struct {
	ID         PickupPresentationID
	IntentID   PickupIntentID
	ReceivedAt time.Time
}

// PickupContextSpeaker is a server-projected conversational origin, never a
// provider system/tool role or an authorization assertion.
type PickupContextSpeaker string

const (
	PickupSpeakerCustomer   PickupContextSpeaker = "customer"
	PickupSpeakerRestaurant PickupContextSpeaker = "restaurant"
)

// PickupContextMessage is an exact stored conversational turn strictly after the
// acknowledged summary/question and before the current inbound. Slice position
// preserves server-stored order; SourceID is opaque stored identity, not authority.
type PickupContextMessage struct {
	SourceID string               `json:"source_id"`
	Speaker  PickupContextSpeaker `json:"speaker"`
	Text     string               `json:"text"`
}

// Detached interpreter input is reconstructed from persisted LOCAL facts. Its
// digest binds the complete ordered History and source identities, actual inbound
// text/event, candidate/presented question, cart version and current control epoch.
// A nonnil empty History means the host established a complete empty window; nil
// is unavailable, never permission to omit required turns. No silent truncation.
// Changing a returned copy grants nothing; implementations reload stored facts.
type PickupCompletionContext struct {
	InboundEventID  string
	InboundText     string
	CandidateID     PickupCandidateID
	CartID          CartID
	CartVersion     Version
	ControlVersion  Version
	SummaryText     string
	ClosingQuestion string
	PresentationID  PickupPresentationID
	IntentID        PickupIntentID
	History         []PickupContextMessage
	Digest          [32]byte
}

type PickupCompletionDecision string

const (
	PickupComplete    PickupCompletionDecision = "complete"
	PickupNotComplete PickupCompletionDecision = "not_complete"
)

// The trusted AI adapter supplies all evidence metadata from stored context and
// server configuration; the v1 model supplies only complete/not_complete. A
// snapshot selector elsewhere grants no authority. Go validates correlation and
// state, not natural-language meaning; no bare confirmed boolean is accepted.
type PickupCompletionEvidence struct {
	CandidateID       PickupCandidateID
	InboundEventID    string
	ContextDigest     [32]byte
	AssessmentVersion string
	Decision          PickupCompletionDecision
}

type PickupReceiptIntent struct {
	ID      PickupIntentID
	OrderID PickupOrderID
	Text    string
}

type PickupSubmission struct {
	OperationID OperationID
	Order       PickupOrder
	Receipt     PickupReceiptIntent
}

// PickupRuntime is an in-process LOCAL port, not an HTTP/provider API. All
// returned values are detached output; implementations reload stored authority.
type PickupRuntime interface {
	Prepare(context.Context, PickupExecution, CartID, Version) (PreparedPickupSummary, error)
	RecordLocalPresentation(context.Context, LocalContext, PickupLocalPresentation) (PickupPresentation, error)
	CompletionContext(context.Context, PickupExecution) (PickupCompletionContext, error)
	Submit(context.Context, PickupExecution, PickupCompletionEvidence) (PickupSubmission, error)
}

type PickupStatus string

const (
	PickupSubmitted PickupStatus = "submitted"
	PickupAccepted  PickupStatus = "accepted"
	PickupPreparing PickupStatus = "preparing"
	PickupReady     PickupStatus = "ready"
	PickupPickedUp  PickupStatus = "picked_up"
	PickupRejected  PickupStatus = "rejected"
	PickupCancelled PickupStatus = "cancelled"
)

type PickupPaymentStatus string

const (
	PickupUnpaid PickupPaymentStatus = "unpaid"
	PickupPaid   PickupPaymentStatus = "paid"
)

type PickupPayment struct {
	State       PickupPaymentStatus
	CollectedAt *time.Time
}

type PickupOrderHeader struct {
	ID        PickupOrderID
	Version   Version
	CreatedAt time.Time
	Status    PickupStatus
	Payment   PickupPayment
	Total     PickupMoney
}

// This slice has exactly pickup/pay_at_pickup fulfillment and independent fee
// basis. Delivery, revisions, linked extras and their links are not implied.
type PickupConfirmedSnapshot struct {
	ID          PickupCandidateID
	UnitCount   int
	Subtotal    PickupMoney
	DeliveryFee PickupMoney
	Total       PickupMoney
}

type PickupOrder struct {
	PickupOrderHeader
	Confirmed            PickupConfirmedSnapshot
	Estimate             *PickupEstimate
	PreparationStartedAt *time.Time
	// PermittedActions is a detached server-computed detail/action-result hint,
	// not an authorization grant. Reads return a nonnil slice; POST revalidates.
	PermittedActions []PickupStaffAction
}

type PickupOrderUnit struct {
	ID     UnitID
	MenuID MenuID
	Name   string
	Note   string
	Price  PickupMoney
}

type PickupPageRequest struct {
	Limit  int
	Cursor string
}

type PickupOrderPage struct {
	Items      []PickupOrderHeader
	NextCursor *string
}

type PickupOrderUnitPage struct {
	Items      []PickupOrderUnit
	NextCursor *string
	SummaryID  PickupCandidateID
	UnitCount  int
}

// The handler derives this opaque auth subject from the verified JWT; the domain
// must still check current restaurant membership before reads/mutations/replay.
type PickupStaffSubject string

type PickupStaffAction string

const (
	PickupAccept         PickupStaffAction = "accept"
	PickupReject         PickupStaffAction = "reject"
	PickupStartPreparing PickupStaffAction = "start_preparing"
	PickupMarkReady      PickupStaffAction = "mark_ready"
	PickupMarkPickedUp   PickupStaffAction = "mark_picked_up"
	PickupCancel         PickupStaffAction = "cancel"
	PickupMarkPaid       PickupStaffAction = "mark_paid"
)

type PickupStaffActionRequest struct {
	Action          PickupStaffAction
	ExpectedVersion Version
	IdempotencyKey  string
}

// PickupStaffPort fixes the domain/HTTP seam so adapter tests need no domain
// implementation. Restaurant configuration belongs to the implementing store,
// never a browser-supplied parameter. All mutations use named actions only.
type PickupStaffPort interface {
	ListOrders(context.Context, PickupStaffSubject, PickupPageRequest) (PickupOrderPage, error)
	ReadOrder(context.Context, PickupStaffSubject, PickupOrderID) (PickupOrder, error)
	ReadUnits(context.Context, PickupStaffSubject, PickupOrderID, PickupCandidateID, PickupPageRequest) (PickupOrderUnitPage, error)
	ApplyAction(context.Context, PickupStaffSubject, PickupOrderID, PickupStaffActionRequest) (PickupOrder, error)
}
