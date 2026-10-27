package cart_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
)

// menuControl uses actual staff verification and membership, never direct SQL
// control insertion. All signing material is fabricated in memory.
func menuControl(t *testing.T, f fixture) (*conversation.Store, string) {
	t.Helper()
	members, err := staff.OpenStore(t.Context(), f.dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(members.Close)
	if created, err := members.EnsureMember(t.Context(), "menu-operator"); err != nil || !created {
		t.Fatalf("owned membership: created=%v error=%v", created, err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","kid":"owned","x":"%s"}]}`, base64.RawURLEncoding.EncodeToString(pub))
	}))
	t.Cleanup(server.Close)
	verifier, err := staff.NewVerifier(server.URL, server.URL+"/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"sub": "menu-operator", "iss": server.URL, "aud": "restaurant-assistant-api", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()})
	token.Header["kid"] = "owned"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	control, err := conversation.OpenStore(t.Context(), f.dsn, restaurant, verifier)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)
	return control, raw
}

// S1/S3 first rule: fresh trusted AI intake atomically pins its original epoch
// and source, with no claim attempt or reply/outbox allocation.
func TestMenuIntakeFreshAIAdmission(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	control, token := menuControl(t, f)
	wantControl := conversation.State{ConversationID: f.local.ConversationID(), Mode: conversation.ModeAI, Version: 0}
	if state, err := control.Initialize(ctx, token, f.local.ConversationID(), conversation.ModeAI, "menu-init"); err != nil || state != wantControl {
		t.Fatalf("authenticated initialize: %+v %v", state, err)
	}
	if state, err := control.Read(ctx, token, f.local.ConversationID()); err != nil || state != wantControl {
		t.Fatalf("authenticated readiness: %+v %v", state, err)
	}
	if local, err := f.s.LoadLocal(ctx, f.local.ConversationID(), "fictional-business", "fictional-sender"); err != nil || local != f.local {
		t.Fatalf("owned local readiness: %+v %v", local, err)
	}
	jobs, err := postgres.Open(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(jobs.Close)
	event := foundation.Event{ID: "menu-first-ai", Sender: "fictional-sender", Text: "  Show the menu\né  "}
	if snapshot, err := jobs.Inspect(ctx, event.ID); !errors.Is(err, foundation.ErrNotFound) || snapshot != (foundation.Snapshot{}) {
		t.Fatalf("fresh event readiness: %+v %v", snapshot, err)
	}
	c := sourceConn(t, f)
	if counts := sourceCounts(t, c); counts != "[0, 0, 0, 0, 0, 0, 0]" {
		t.Fatalf("empty intake readiness: %s", counts)
	}
	t.Log("fixture ready: migrated owned catalog/local scope, current membership, authenticated AI control version 0, absent event and empty intake")
	receipt, created, err := f.s.AcceptMenu(ctx, f.local, event, time.Now().UTC())
	if err != nil || !created {
		t.Fatalf("fresh trusted AI admission must commit queued work: created=%v receipt=%+v error=%v", created, receipt, err)
	}
	source := receipt.Source
	if source.RestaurantID() != restaurant || source.ConversationID() != f.local.ConversationID() || source.EventProviderID() != event.ID || source.EventUUID() == "" || source.Business() != "fictional-business" || source.Sender() != event.Sender || receipt.JobID == "" || receipt.Mode != "ai" || receipt.Version != 0 || receipt.State != "queued" || receipt.NoReplyReason != "" {
		t.Fatalf("exact admission receipt: %+v source=%+v", receipt, source)
	}
	if snapshot, err := jobs.Inspect(ctx, event.ID); err != nil || snapshot != (foundation.Snapshot{Event: event, JobState: "queued", JobCount: 1}) {
		t.Fatalf("exact public persisted effects: %+v %v", snapshot, err)
	}
	if bound, err := f.s.BindSource(ctx, f.local, event.ID); err != nil || bound != source {
		t.Fatalf("persisted source identity: %+v %v", bound, err)
	}
	if read, err := f.s.ReadAdmission(ctx, f.local, event.ID); err != nil || read != receipt {
		t.Fatalf("locked public admission receipt: %+v %v", read, err)
	}
	var eid, jid, route, mode string
	var sequence, next, version, generation int64
	if err := c.QueryRow(ctx, `SELECT e.id::text,j.id::text,e.sequence,q.next_sequence,j.route,j.admission_mode,j.admission_version,j.generation FROM app.inbound_events e JOIN app.jobs j ON j.event_id=e.id JOIN app.conversation_sequences q ON q.sender=e.sender WHERE e.provider_event_id=$1`, event.ID).Scan(&eid, &jid, &sequence, &next, &route, &mode, &version, &generation); err != nil {
		t.Fatal(err)
	}
	if eid != source.EventUUID() || jid != receipt.JobID || sequence != 1 || next != 1 || route != "menu_message" || mode != "ai" || version != 0 || generation != 0 || sourceCounts(t, c) != "[1, 0, 0, 0, 1, 0, 0]" {
		t.Fatalf("allocation integrity: event=%s job=%s sequence=%d counter=%d route=%s mode=%s version=%d generation=%d counts=%s", eid, jid, sequence, next, route, mode, version, generation, sourceCounts(t, c))
	}
	if state, err := control.Read(ctx, token, f.local.ConversationID()); err != nil || state != wantControl {
		t.Fatalf("admission changed authority: %+v %v", state, err)
	}
}
