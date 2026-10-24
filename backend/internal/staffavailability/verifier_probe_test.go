package staffavailability_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

// S1/S2/S4: contradictory same-key requests must not become two writes.
func TestVerifierConflictingSameKeyAndFailureReuse(t *testing.T) {
	f := setup(t)
	snapshot := func() [32]byte {
		t.Helper()
		page, err := f.store.Page(t.Context(), 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		base, err := f.base.Inspect(t.Context(), "availability-preservation")
		if err != nil {
			t.Fatal(err)
		}
		var saved []string
		if err = f.pool.QueryRow(t.Context(), "SELECT ARRAY(SELECT response_body FROM app.operations ORDER BY response_body)").Scan(&saved); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal([]any{page, base, saved})
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(raw)
	}
	for _, target := range []struct{ path, field string }{{itemPath, "available"}, {settingsPath, "open"}} {
		body := func(v int, value bool) string {
			return fmt.Sprintf(`{"expected_version":"%d","%s":%t}`, v, target.field, value)
		}
		key := func(k string) map[string][]string { return map[string][]string{"Idempotency-Key": {k}} }
		out := make([]*httptest.ResponseRecorder, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				out[i] = request(t, f.handler, t.Context(), "PATCH", target.path, body(0, i == 0), f.token, key("contradictory"))
			}(i)
		}
		close(start)
		wg.Wait()
		winner := -1
		for i, w := range out {
			if w.Code == 200 {
				if winner != -1 {
					t.Fatal("two winners")
				}
				winner = i
				response(t, w, 200, "", "")
			} else {
				response(t, w, 409, "idempotency_conflict", "Idempotency key was already used for a different request.")
			}
		}
		if winner == -1 {
			t.Fatal("no winner")
		}
		before := snapshot()
		rejected := request(t, f.handler, t.Context(), "PATCH", target.path, body(0, winner != 0), f.token, key("contradictory"))
		response(t, rejected, 409, "idempotency_conflict", "Idempotency key was already used for a different request.")
		if snapshot() != before {
			t.Fatal("changed-key rejection changed persisted exports/results")
		}
		stale := request(t, f.handler, t.Context(), "PATCH", target.path, body(0, true), f.token, key("reusable"))
		response(t, stale, 409, "version_conflict", "Refresh availability before trying again.")
		if snapshot() != before {
			t.Fatal("stale rejection changed persisted exports/results")
		}
		// Corrected version reuses the rejected key; fresh same-value intent increments.
		fresh := request(t, f.handler, t.Context(), "PATCH", target.path, body(1, winner == 0), f.token, key("reusable"))
		response(t, fresh, 200, "", "")
		var result struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(fresh.Body.Bytes(), &result); err != nil || result.Data["version"] != "2" {
			t.Fatal("fresh intent did not increment exactly once")
		}
		after := snapshot()
		replay := request(t, f.handler, t.Context(), "PATCH", target.path, body(0, winner == 0), f.token, key("contradictory"))
		response(t, replay, 200, "", "")
		if replay.Body.String() != out[winner].Body.String() || snapshot() != after {
			t.Fatal("historical replay changed bytes or persisted state")
		}
	}
	page, err := f.store.Page(t.Context(), 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Settings.Version != 2 || len(page.Items) != 1 || page.Items[0].Version != 2 || page.Items[0].ID != itemID || page.Items[0].Name != "料理 <&>" || page.Items[0].PriceMinor != 9223372036854775807 || page.Settings.Currency != "KWD" || page.Settings.CurrencyExponent != 3 || page.Settings.FlatFeeMinor != 17 {
		t.Fatal("public page lost control state or immutable catalog values")
	}
	var count int
	if err = f.pool.QueryRow(t.Context(), "SELECT count(*) FROM app.operations").Scan(&count); err != nil || count != 4 {
		t.Fatal("result count not one per accepted intent")
	}
	base, err := f.base.Inspect(t.Context(), "availability-preservation")
	if err != nil || !reflect.DeepEqual(base, f.baseline) {
		t.Fatal("foundation/outbound changed")
	}
}
