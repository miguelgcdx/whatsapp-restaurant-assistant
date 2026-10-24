package staffavailability_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/staffavailability"
)

type membershipFunc func(context.Context, string) (bool, error)

func (f membershipFunc) Access(ctx context.Context, s string) (bool, error) { return f(ctx, s) }

func TestDependenciesAndRollback(t *testing.T) {
	f := setup(t)
	beforeSettings, err := f.store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	beforeMenu, err := f.store.Menu(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func() {
		t.Helper()
		s, err := f.store.Settings(t.Context())
		if err != nil || s != beforeSettings {
			t.Fatal("settings changed", s, err)
		}
		m, err := f.store.Menu(t.Context())
		if err != nil || !reflect.DeepEqual(m, beforeMenu) {
			t.Fatal("menu changed", m, err)
		}
		var count int
		if err = f.pool.QueryRow(t.Context(), "SELECT count(*) FROM app.operations").Scan(&count); err != nil || count != 0 {
			t.Fatal("committed operation", count, err)
		}
		after, err := f.base.Inspect(t.Context(), "availability-preservation")
		if err != nil || !reflect.DeepEqual(after, f.baseline) {
			t.Fatal("foundation/outbound changed", err)
		}
	}
	body := `{"expected_version":"0","available":false}`
	for _, authority := range []membershipFunc{
		func(context.Context, string) (bool, error) { return false, nil },
		func(context.Context, string) (bool, error) { return false, errors.New("secret-sentinel provider sql") },
	} {
		h, err := staffavailability.NewHandler(f.store, authority, f.verifier)
		if err != nil {
			t.Fatal(err)
		}
		active, e := authority(t.Context(), "alex")
		_ = active
		if e == nil {
			response(t, request(t, h, t.Context(), "PATCH", itemPath, body, f.token, nil), 403, "forbidden", "Staff access is not available.")
		} else {
			response(t, request(t, h, t.Context(), "PATCH", itemPath, body, f.token, nil), 503, "temporarily_unavailable", "Availability is temporarily unavailable.")
		}
		assertUnchanged()
	}
	downStore, err := catalog.OpenStore(t.Context(), "postgres://127.0.0.1:1/restaurant_foundation_absent?sslmode=disable", restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer downStore.Close()
	h, err := staffavailability.NewHandler(downStore, f.members, f.verifier)
	if err != nil {
		t.Fatal(err)
	}
	response(t, request(t, h, t.Context(), "PATCH", itemPath, body, f.token, nil), 503, "temporarily_unavailable", "Availability is temporarily unavailable.")
	assertUnchanged()
	downMembers, err := staff.OpenStore(t.Context(), "postgres://127.0.0.1:1/restaurant_foundation_absent?sslmode=disable", restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer downMembers.Close()
	h, err = staffavailability.NewHandler(f.store, downMembers, f.verifier)
	if err != nil {
		t.Fatal(err)
	}
	response(t, request(t, h, t.Context(), "PATCH", itemPath, body, f.token, nil), 503, "temporarily_unavailable", "Availability is temporarily unavailable.")
	assertUnchanged()
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer keys.Close()
	v, err := staff.NewVerifier("http://127.0.0.1:3000", keys.URL+"/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	h, err = staffavailability.NewHandler(f.store, f.members, v)
	if err != nil {
		t.Fatal(err)
	}
	response(t, request(t, h, t.Context(), "PATCH", itemPath, body, f.token, nil), 503, "temporarily_unavailable", "Availability is temporarily unavailable.")
	assertUnchanged()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	response(t, request(t, f.handler, ctx, "PATCH", itemPath, body, f.token, nil), 503, "temporarily_unavailable", "Availability is temporarily unavailable.")
	assertUnchanged()
	// Membership creates identity only. Missing operational settings remain absent.
	cfg := f.pool.Config().ConnConfig.ConnString()
	missingMembers, err := staff.OpenStore(t.Context(), cfg, other)
	if err != nil {
		t.Fatal(err)
	}
	defer missingMembers.Close()
	if _, err = missingMembers.EnsureMember(t.Context(), "alex"); err != nil {
		t.Fatal(err)
	}
	missingStore, err := catalog.OpenStore(t.Context(), cfg, other)
	if err != nil {
		t.Fatal(err)
	}
	defer missingStore.Close()
	h, err = staffavailability.NewHandler(missingStore, missingMembers, f.verifier)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ path, body string }{{itemPath, body}, {settingsPath, `{"expected_version":"0","open":false}`}} {
		response(t, request(t, h, t.Context(), "PATCH", target.path, target.body, f.token, nil), 503, "temporarily_unavailable", "Availability is temporarily unavailable.")
	}
	if s, err := missingStore.Settings(t.Context()); err != catalog.ErrNotFound || s != (catalog.Settings{}) {
		t.Fatal("implicit settings", s, err)
	}
	if m, err := missingStore.Menu(t.Context()); err != nil || len(m) != 0 {
		t.Fatal("implicit menu", m, err)
	}
	assertUnchanged()
	// Cross-scope item existence must not be disclosed.
	if err = missingStore.Initialize(t.Context(), beforeSettings, nil); err != nil {
		t.Fatal(err)
	}
	response(t, request(t, h, t.Context(), "PATCH", itemPath, body, f.token, nil), 404, "not_found", "Catalog item was not found.")
	assertUnchanged()
	// A legitimate PostgreSQL value violating Unicode nonblank output validation
	// fails after the CAS, so the entire transaction (including its version) rolls back.
	if _, err = f.pool.Exec(t.Context(), "UPDATE app.menu_items SET name=$1 WHERE id=$2", "\u2003", itemID); err != nil {
		t.Fatal(err)
	}
	response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, body, f.token, nil), 500, "internal_error", "Availability could not be completed.")
	m, err := f.store.Menu(t.Context())
	if err != nil || m[0].Version != 0 || !m[0].Available {
		t.Fatal("invalid output committed mutation", m, err)
	}
	if _, err = f.pool.Exec(t.Context(), "UPDATE app.menu_items SET name=$1 WHERE id=$2", beforeMenu[0].Name, itemID); err != nil {
		t.Fatal(err)
	}
	assertUnchanged()
	// The same key now succeeds: the failed result did not strand a claim.
	response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, body, f.token, nil), 200, "", "")
	// A corrupt complete result is never emitted as a successful replay.
	if _, err = f.pool.Exec(t.Context(), `UPDATE app.operations SET response_body='{"data":null,"request_id":"corrupt"}' WHERE restaurant_id=$1`, restaurant); err != nil {
		t.Fatal(err)
	}
	response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, body, f.token, nil), 500, "internal_error", "Availability could not be completed.")
	m, err = f.store.Menu(t.Context())
	if err != nil || m[0].Version != 1 || m[0].Available {
		t.Fatal("bad replay changed state", m, err)
	}
}

func TestReplayWaitsForIdentityAndFreshAuthority(t *testing.T) {
	f := setup(t)
	body := `{"expected_version":"0","available":false}`
	response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, body, f.token, nil), 200, "", "")
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), "SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE", restaurant); err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	h, err := staffavailability.NewHandler(f.store, membershipFunc(func(ctx context.Context, s string) (bool, error) { checks.Add(1); return f.members.Access(ctx, s) }), f.verifier)
	if err != nil {
		t.Fatal(err)
	}
	// Bound the wait without polling or sleeps. Even a cached result must wait for
	// the identity lock, and cancellation must not call membership prematurely.
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	response(t, request(t, h, ctx, "PATCH", itemPath, body, f.token, nil), 503, "temporarily_unavailable", "Availability is temporarily unavailable.")
	if checks.Load() != 0 {
		t.Fatal("membership checked before obtaining identity lock")
	}
	if err = f.members.RevokeMember(t.Context(), "alex"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	response(t, request(t, h, t.Context(), "PATCH", itemPath, body, f.token, nil), 403, "forbidden", "Staff access is not available.")
	if checks.Load() != 1 {
		t.Fatal("fresh authority not checked")
	}
	m, err := f.store.Menu(t.Context())
	if err != nil || m[0].Version != 1 || m[0].Available {
		t.Fatal("replay/revocation changed state", m, err)
	}
	var count int
	if err = f.pool.QueryRow(t.Context(), "SELECT count(*) FROM app.operations").Scan(&count); err != nil || count != 1 {
		t.Fatal("replay wrote result", count, err)
	}
}

// TestLiveReplayRechecksRevocationAfterIdentityLock observes the same HTTP
// request's dedicated backend waiting on the actual identity-row lock. Unlike
// the cancellation test above, it never cancels/replaces that pending request.
func TestLiveReplayRechecksRevocationAfterIdentityLock(t *testing.T) {
	f := setup(t)
	// A dedicated pool label ties pg_stat_activity to this single replay request;
	// no other request or fixture connection uses this application_name.
	const application = "staff_availability_live_replay"
	u, err := url.Parse(f.pool.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("application_name", application)
	u.RawQuery = query.Encode()
	store, err := catalog.OpenStore(t.Context(), u.String(), restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initialHandler, err := staffavailability.NewHandler(store, f.members, f.verifier)
	if err != nil {
		t.Fatal(err)
	}
	const body = `{"expected_version":"0","available":false}`
	committed := request(t, initialHandler, t.Context(), "PATCH", itemPath, body, f.token, nil)
	response(t, committed, 200, "", "")
	beforeSettings, err := f.store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	beforeMenu, err := f.store.Menu(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var beforeCount int
	if err = f.pool.QueryRow(t.Context(), "SELECT count(*) FROM app.operations").Scan(&beforeCount); err != nil || beforeCount != 1 {
		t.Fatalf("expected one committed replay result: %d %v", beforeCount, err)
	}

	holder, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(t.Context()) }()
	var holderPID int32
	if err = holder.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	var lockedID string
	if err = holder.QueryRow(t.Context(), "SELECT id::text FROM app.restaurants WHERE id=$1 FOR UPDATE", restaurant).Scan(&lockedID); err != nil || lockedID != restaurant {
		t.Fatalf("identity lock: %s %v", lockedID, err)
	}

	var checks atomic.Int32
	authorityContext := make(chan error, 1)
	h, err := staffavailability.NewHandler(store, membershipFunc(func(ctx context.Context, subject string) (bool, error) {
		checks.Add(1)
		active, accessErr := f.members.Access(ctx, subject)
		authorityContext <- ctx.Err()
		return active, accessErr
	}), f.verifier)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- r
		h.ServeHTTP(w, r)
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer func() {
		cancel()
		_ = holder.Rollback(t.Context())
		server.Close()
	}()
	pending, err := http.NewRequestWithContext(ctx, "PATCH", server.URL+itemPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	pending.Header.Set("Authorization", "Bearer "+f.token)
	pending.Header.Set("Content-Type", "application/json")
	pending.Header.Set("Idempotency-Key", "intent")
	type outcome struct {
		response *http.Response
		err      error
	}
	finished := make(chan outcome, 1)
	go func() {
		res, requestErr := server.Client().Do(pending)
		finished <- outcome{res, requestErr}
	}()
	var serving *http.Request
	select {
	case serving = <-entered:
	case <-ctx.Done():
		t.Fatal("HTTP request did not enter the handler before its deadline")
	}
	assertPending := func() {
		t.Helper()
		if ctx.Err() != nil || serving.Context().Err() != nil {
			t.Fatalf("replay context stopped while pending: client=%v server=%v", ctx.Err(), serving.Context().Err())
		}
		select {
		case early := <-finished:
			if early.response != nil {
				early.response.Body.Close()
			}
			t.Fatalf("same replay finished before identity-lock release: response=%v error=%v", early.response, early.err)
		default:
		}
		if checks.Load() != 0 {
			t.Fatal("membership checked before identity-lock release")
		}
	}
	// Observation is confined to this database, dedicated request pool, exact
	// production lock query, and known holder. An unrelated waiter cannot match.
	const blockedQuery = `SELECT ARRAY(SELECT a.pid FROM pg_stat_activity a
 WHERE a.datname=current_database() AND a.usename=current_user
 AND a.application_name=$1 AND a.state='active' AND a.wait_event_type='Lock'
 AND a.query=$2 AND $3::integer=ANY(pg_blocking_pids(a.pid))
 AND ($4::integer=0 OR a.pid=$4))`
	waitCtx, stopObservation := context.WithTimeout(ctx, 3*time.Second)
	defer stopObservation()
	var waiterPID int32
	for waiterPID == 0 {
		assertPending()
		var waiting []int32
		if err = f.pool.QueryRow(waitCtx, blockedQuery, application, "SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE", holderPID, int32(0)).Scan(&waiting); err != nil {
			t.Fatalf("could not observe same live replay blocked by holder: %v", err)
		}
		assertPending()
		if len(waiting) > 1 {
			t.Fatalf("ambiguous dedicated replay backend: %v", waiting)
		}
		if len(waiting) == 1 {
			waiterPID = waiting[0]
		}
	}
	if waiterPID == holderPID {
		t.Fatal("waiter was not a separate backend")
	}
	t.Logf("Observed live replay backend %d blocked by holder %d; membership checks=0", waiterPID, holderPID)
	if err = f.members.RevokeMember(ctx, "alex"); err != nil {
		t.Fatal(err)
	}
	if active, accessErr := f.members.Access(ctx, "alex"); accessErr != nil || active {
		t.Fatalf("public membership revocation was not committed: active=%t error=%v", active, accessErr)
	}
	// Confirm the exact previously observed backend is STILL waiting after the
	// public revocation commits, rather than having completed/canceled meanwhile.
	assertPending()
	var stillWaiting []int32
	if err = f.pool.QueryRow(waitCtx, blockedQuery, application, "SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE", holderPID, waiterPID).Scan(&stillWaiting); err != nil || len(stillWaiting) != 1 || stillWaiting[0] != waiterPID {
		t.Fatalf("same replay did not remain blocked during revocation: %v %v", stillWaiting, err)
	}
	assertPending()
	if err = holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("client context stopped during lock release: %v", ctx.Err())
	}
	var result outcome
	select {
	case result = <-finished:
	case <-ctx.Done():
		t.Fatal("same replay did not finish after lock release")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer result.response.Body.Close()
	if result.response.Request != pending || serving.URL.Path != itemPath || len(entered) != 0 {
		t.Fatal("response did not belong to the single pending client request")
	}
	payload, err := io.ReadAll(io.LimitReader(result.response.Body, 512*1024+1))
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("client request was canceled/deadlined: %v", ctx.Err())
	}
	if checks.Load() != 1 {
		t.Fatalf("expected one fresh membership check after lock release, got %d", checks.Load())
	}
	select {
	case authorityErr := <-authorityContext:
		if authorityErr != nil {
			t.Fatalf("membership callback context was not live: %v", authorityErr)
		}
	default:
		t.Fatal("fresh membership check did not finish")
	}
	recorded := httptest.NewRecorder()
	for name, values := range result.response.Header {
		recorded.Header()[name] = values
	}
	recorded.WriteHeader(result.response.StatusCode)
	_, _ = recorded.Body.Write(payload)
	response(t, recorded, 403, "forbidden", "Staff access is not available.")
	if recorded.Body.String() == committed.Body.String() {
		t.Fatal("revoked replay returned cached success")
	}
	settings, err := f.store.Settings(t.Context())
	if err != nil || settings != beforeSettings {
		t.Fatalf("replay changed settings: %+v %v", settings, err)
	}
	menu, err := f.store.Menu(t.Context())
	if err != nil || !reflect.DeepEqual(menu, beforeMenu) {
		t.Fatalf("replay changed catalog: %+v %v", menu, err)
	}
	var afterCount int
	if err = f.pool.QueryRow(t.Context(), "SELECT count(*) FROM app.operations").Scan(&afterCount); err != nil || afterCount != beforeCount {
		t.Fatalf("replay changed committed result count: %d %v", afterCount, err)
	}
	var saved string
	if err = f.pool.QueryRow(t.Context(), `SELECT response_body FROM app.operations WHERE restaurant_id=$1 AND auth_user_id='alex' AND method='PATCH' AND resource=$2 AND idempotency_key='intent'`, restaurant, itemPath).Scan(&saved); err != nil || saved != committed.Body.String() {
		t.Fatalf("replay changed committed payload: %v", err)
	}
	after, err := f.base.Inspect(t.Context(), "availability-preservation")
	if err != nil || !reflect.DeepEqual(after, f.baseline) {
		t.Fatal("replay changed foundation/outbound state", err)
	}
	t.Log("Same live HTTP replay returned safe/no-store 403 after revocation and lock release; catalog/settings/result/foundation state unchanged")
}

func TestWireBoundariesBothControls(t *testing.T) {
	f := setup(t)
	for _, target := range []struct{ path, field string }{{itemPath, "available"}, {settingsPath, "open"}} {
		for _, body := range []string{`{"expected_version":"0"}`, `{"expected_version":"0","` + target.field + `":null}`, `{"expected_version":"0","` + target.field + `":true,"` + target.field + `":false}`} {
			response(t, request(t, f.handler, t.Context(), "PATCH", target.path, body, f.token, nil), 400, "invalid_input", "Invalid request.")
		}
		body := `{"expected_version":"0","` + target.field + `":true}`
		padded := body + strings.Repeat(" ", 65536-len(body))
		w := request(t, f.handler, t.Context(), "PATCH", target.path, padded, f.token, map[string][]string{"Idempotency-Key": {strings.Repeat("~", 128)}})
		response(t, w, 200, "", "")
		// Exactly 64 KiB raw JSON and a 128-byte key are accepted, same value increments.
		if !strings.Contains(w.Body.String(), `"version":"1"`) {
			t.Fatal(w.Body.String())
		}
		response(t, request(t, f.handler, t.Context(), "PATCH", target.path, `{"expected_version":"1","`+target.field+`":false}`, f.token, map[string][]string{"Idempotency-Key": {"!"}}), 200, "", "")
	}
	// Exhausted stored versions reject both aggregates, never overflow or reserve a key.
	for _, table := range []string{"app.menu_items", "app.restaurant_settings"} {
		if _, err := f.pool.Exec(t.Context(), "UPDATE "+table+" SET version=9223372036854775807 WHERE restaurant_id=$1", restaurant); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []struct{ path, field string }{{itemPath, "available"}, {settingsPath, "open"}} {
		response(t, request(t, f.handler, t.Context(), "PATCH", target.path, `{"expected_version":"9223372036854775807","`+target.field+`":true}`, f.token, map[string][]string{"Idempotency-Key": {"exhausted"}}), 409, "version_conflict", "Refresh availability before trying again.")
		// Exhaustion cannot suppress a complete authorized replay with an older version.
		response(t, request(t, f.handler, t.Context(), "PATCH", target.path, `{"expected_version":"0","`+target.field+`":true}`, f.token, map[string][]string{"Idempotency-Key": {strings.Repeat("~", 128)}}), 200, "", "")
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM app.operations").Scan(&count); err != nil || count != 4 {
		t.Fatal("overflow wrote operation", count, err)
	}
	settings, err := f.store.Settings(t.Context())
	if err != nil || settings.Version != 9223372036854775807 || settings.Open {
		t.Fatal(settings, err)
	}
	menu, err := f.store.Menu(t.Context())
	if err != nil || menu[0].Version != 9223372036854775807 || menu[0].Available {
		t.Fatal(menu, err)
	}
}
