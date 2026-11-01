package menuengine_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/catalog"
	cc "restaurant-assistant/backend/internal/commandcode"
)

// These scripted contradictions characterize pass-through, not model quality.
func TestMenuTurnProposalBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		withMenu  bool
		reply     string
		wantCalls int
		wantReads int
	}{
		{"direct open claim is ungrounded", false, "We are open.", 1, 0},
		{"closed snapshot contradicts prose", true, "We are open. Water costs USD 9.99.", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := menuFixture() // Closed, with Water priced at zero.
			before := snapshot
			before.Items = append([]catalog.Item(nil), snapshot.Items...)
			wantPayload := expectedWire(t, before)
			reads := 0
			c, tr := testClient(t, func(n int, w wireRequest) (*http.Response, error) {
				if n > tc.wantCalls {
					t.Fatalf("unexpected model attempt %d", n)
				}
				if tc.withMenu && n == 1 {
					return response(toolMessage(call()), "tool_calls"), nil
				}
				if tc.withMenu {
					if len(w.Messages) != 4 {
						t.Fatalf("continuation has %d messages", len(w.Messages))
					}
					tool := w.Messages[3]
					if tool.Role != cc.RoleTool || tool.ToolCallID != call().ID || tool.Content == nil || *tool.Content != wantPayload {
						t.Fatal("authoritative closed menu/prices/availability changed")
					}
				}
				return response(cc.Message{Role: cc.RoleAssistant, Content: ptr(tc.reply)}, "stop"), nil
			})
			e := newEngine(t, c, func(context.Context) (catalog.MenuSnapshot, error) {
				reads++
				if reads > tc.wantReads {
					t.Fatal("unexpected menu read")
				}
				return snapshot, nil
			})
			got, err := e.Run(t.Context(), "Are you open? How much is Water?")
			if err != nil {
				t.Fatalf("characterization returned error: %v", err)
			}
			if got.Reply != tc.reply {
				t.Fatalf("proposal changed: got %q, want %q", got.Reply, tc.reply)
			}
			if tc.withMenu {
				if got.Menu == nil || got.Menu.Open {
					t.Fatalf("closed structured authority lost: %+v", got.Menu)
				}
			} else if got.Menu != nil {
				t.Fatalf("direct prose acquired menu authority: %+v", got.Menu)
			}
			if tr.count() != tc.wantCalls || reads != tc.wantReads {
				t.Fatalf("effects: attempts=%d reads=%d; want %d/%d", tr.count(), reads, tc.wantCalls, tc.wantReads)
			}
			if !reflect.DeepEqual(snapshot, before) {
				t.Fatal("source snapshot changed")
			}
		})
	}
}
