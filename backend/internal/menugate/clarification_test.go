package menugate_test

import (
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	cc "restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menugate"
)

func clarificationClaim(t *testing.T, f *fixture) (turn, cart.MenuClaim, cart.Snapshot) {
	t.Helper()
	local, _ := nativeLocal(t, f)
	made, err := f.cart.Create(t.Context(), local, newClarificationOperation(t))
	if err != nil {
		t.Fatal(err)
	}
	event := foundation.Event{ID: "clarification-" + newClarificationOperation(t).String(), Sender: "native-" + strconv.Itoa(f.next), Text: "  remove that one 🥪\né  "}
	_, fresh, err := f.cart.AcceptMenu(t.Context(), local, event, time.Now())
	if err != nil || !fresh {
		t.Fatal(fresh, err)
	}
	claim, ok, err := f.cart.ClaimMenu(t.Context(), local, time.Minute)
	if err != nil || !ok || claim.Job().Event != event {
		t.Fatal(ok, err)
	}
	return turn{local, claim.Source(), claim.Job()}, claim, made.Cart
}
func newClarificationOperation(t *testing.T) cart.OperationID {
	t.Helper()
	id, err := cart.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAIAmbiguityQueueStoredKindAndSourceBoundReceipt(t *testing.T) {
	for _, tc := range []struct {
		kind     cart.UnresolvedEditKind
		question string
	}{
		{cart.UnresolvedAdd, "Which item would you like to add?"},
		{cart.UnresolvedRemove, "Which item would you like to remove?"},
		{cart.UnresolvedSetNote, "Which item should I change the note for?"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			f := setup(t)
			v, claim, s := clarificationClaim(t, f)
			marker, err := f.cart.MarkUnresolvedEdit(t.Context(), v.local, claim, 4, s.ID, s.Version, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) {
				calls++
				t.Error("clarification invoked unrelated menuengine")
				return answer(), nil
			}))
			got, err := g.QueueClarification(t.Context(), claim, 4)
			if err != nil || got.OutboxID == "" || got.Content != tc.question || got.State != "pending" || got.ProviderID != "" || got.Attempt != 0 || got.ControlVersion != 0 {
				t.Fatalf("queued only: %+v %v", got, err)
			}
			if read, err := g.ReadQueued(t.Context(), claim.Source()); err != nil || read != got {
				t.Fatal(read, err)
			}
			admission, err := f.cart.ReadAdmission(t.Context(), v.local, v.job.Event.ID)
			if err != nil || admission.Source != claim.Source() || admission.OutboxID != got.OutboxID || admission.Reply != tc.question || admission.State != "completed" {
				t.Fatal(admission, err)
			}
			if job := f.inspect(t, v); job.JobState != "completed" || job.OutboxCount != 1 || job.OutboxState != "pending" || job.JobAttempt != 1 {
				t.Fatal(job)
			}
			current, err := f.cart.Read(t.Context(), v.local, s.ID)
			if err != nil || !reflect.DeepEqual(current, s) {
				t.Fatal(current, err)
			}
			// A new live source can inspect the marker after the queue retired its source.
			event := foundation.Event{ID: "inspect-" + newClarificationOperation(t).String(), Sender: v.job.Event.Sender, Text: "still unresolved"}
			_, _, err = f.cart.AcceptMenu(t.Context(), v.local, event, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			next, ok, err := f.cart.ClaimMenu(t.Context(), v.local, time.Minute)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			view, err := f.cart.ReadPendingEdits(t.Context(), v.local, next, s.ID, s.Version)
			if err != nil || !reflect.DeepEqual(view.Entries(), []cart.PendingEdit{{Ref: marker, Kind: tc.kind, Request: v.job.Event.Text}}) || calls != 0 {
				t.Fatalf("queue is not delivery/resolution: %+v %v calls=%d", view.Entries(), err, calls)
			}
		})
	}
}

func TestAIAmbiguityQueueRejectsMissingMarkerAndChangedControl(t *testing.T) {
	for _, which := range []string{"missing-marker", "wrong-slot", "changed-control"} {
		t.Run(which, func(t *testing.T) {
			f := setup(t)
			v, claim, s := clarificationClaim(t, f)
			want := menugate.ErrNotFound
			if which == "wrong-slot" {
				if _, err := f.cart.MarkUnresolvedEdit(t.Context(), v.local, claim, 5, s.ID, s.Version, cart.UnresolvedRemove); err != nil {
					t.Fatal(err)
				}
			}
			if which == "changed-control" {
				if _, err := f.cart.MarkUnresolvedEdit(t.Context(), v.local, claim, 4, s.ID, s.Version, cart.UnresolvedRemove); err != nil {
					t.Fatal(err)
				}
				f.mode(t, v, conversation.ModeStaff)
				want = menugate.ErrControlChanged
			}
			before := f.inspect(t, v)
			admission, err := f.cart.ReadAdmission(t.Context(), v.local, v.job.Event.ID)
			if err != nil {
				t.Fatal(err)
			}
			g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { t.Error("rejection called model"); return answer(), nil }))
			got, err := g.QueueClarification(t.Context(), claim, 4)
			if !errors.Is(err, want) || got != (menugate.Receipt{}) || f.inspect(t, v) != before {
				t.Fatal(got, err)
			}
			after, err := f.cart.ReadAdmission(t.Context(), v.local, v.job.Event.ID)
			if err != nil || after != admission {
				t.Fatal(after, err)
			}
			current, err := f.cart.Read(t.Context(), v.local, s.ID)
			if err != nil || !reflect.DeepEqual(current, s) {
				t.Fatal(current, err)
			}
		})
	}
}

// Preservation for the planned shared commit helper, not an old Go7 assessment.
func TestAIAmbiguityLegacyRunAndQueuePreserved(t *testing.T) {
	f := setup(t)
	v := f.fresh(t, conversation.ModeAI)
	calls := 0
	const reply = "  unchanged legacy reply\né  "
	g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) {
		calls++
		return response(cc.Message{Role: cc.RoleAssistant, Content: ptr(reply)}, "stop"), nil
	}))
	queued(t, f, v, g, reply)
	if calls != 1 {
		t.Fatal(calls)
	}
}
