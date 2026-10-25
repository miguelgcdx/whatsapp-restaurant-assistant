package integration_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/api"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

func TestSignedLocalCartExecution(t *testing.T) {
	ctx := t.Context()
	dsn := testpg.Start(t)
	if err := postgres.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	jobs, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	key := []byte(strings.Repeat("x", 32))
	h, err := api.New(jobs, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"event_id":"native-cart","sender":"fixture-sender","text":"opaque; NOT a cart command"}`
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	req := httptest.NewRequest("POST", "/synthetic/events", strings.NewReader(body))
	req.Header.Set("X-Synthetic-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != 200 || response.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("ingress=%d %q", response.Code, response.Body.String())
	}
	const restaurant = "b0000000-0000-4000-8000-000000000006"
	const menu = "10000000-0000-4000-8000-000000000001"
	cat, err := catalog.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	if err = cat.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, Open: true}, []catalog.Item{{ID: menu, Name: "Fictional", PriceMinor: 650, Currency: "USD", Available: true}}); err != nil {
		t.Fatal(err)
	}
	s, err := cart.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local, err := s.ProvisionLocal(ctx, "fixture-business", "fixture-sender")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.RegisterExecution(ctx, local, "native-cart", 0, cart.CreateCall())
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := jobs.ClaimJob(ctx, time.Now(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%v %v", ok, err)
	}
	created, err := s.Execute(ctx, local, plan, job)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := cart.ParseMenuID(menu)
	if err != nil {
		t.Fatal(err)
	}
	action, err := cart.Add(mid, 2)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := cart.EditCall(created.Cart.ID, created.Cart.Version, action)
	if err != nil {
		t.Fatal(err)
	}
	editPlan, err := s.RegisterExecution(ctx, local, "native-cart", 1, edit)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.Execute(ctx, local, editPlan, job)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Cart.Subtotal != 1300 || changed.Cart.Version.Int64() != 1 || len(changed.Cart.Units) != 2 {
		t.Fatalf("edit=%+v", changed)
	}
	readCall, err := cart.ReadCall(created.Cart.ID)
	if err != nil {
		t.Fatal(err)
	}
	readPlan, err := s.RegisterExecution(ctx, local, "native-cart", 2, readCall)
	if err != nil {
		t.Fatal(err)
	}
	boundRead, err := s.Execute(ctx, local, readPlan, job)
	if err != nil || !reflect.DeepEqual(boundRead.Cart, changed.Cart) {
		t.Fatalf("bound read=%+v %v", boundRead, err)
	}
	read, err := s.Read(ctx, local, created.Cart.ID)
	if err != nil || !reflect.DeepEqual(read, changed.Cart) {
		t.Fatalf("read=%+v %v", read, err)
	}
	inspected, err := jobs.Inspect(ctx, "native-cart")
	if err != nil || inspected.JobState != "leased" || inspected.OutboxCount != 0 || inspected.Event.Text != "opaque; NOT a cart command" {
		t.Fatalf("no dispatch=%+v %v", inspected, err)
	}
	if err = jobs.CompleteJob(ctx, job, "Local test acknowledgement.", time.Now()); err != nil {
		t.Fatal(err)
	}
	inspected, err = jobs.Inspect(ctx, "native-cart")
	if err != nil || inspected.JobState != "completed" || inspected.OutboxCount != 1 {
		t.Fatalf("legacy ack=%+v %v", inspected, err)
	}
}
