package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menugate"
	"restaurant-assistant/backend/internal/pickupintent"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpickupreceiver"
)

// TestOrdersBrowserSeed is an opt-in, test-owned entrypoint, not an auth bypass.
// With STAFF_ORDERS_BROWSER_SEED_INPUT absent it explicitly skips normal suites.
// Otherwise require STAFF_ORDERS_BROWSER_SEED_OUTPUT; both absolute paths must be
// direct children of the same new, caller-owned /tmp run directory (0700).
// Input is a private regular nonsymlink 0600 JSON file; output must not exist and
// is created O_EXCL 0600 only after persisted public-API success. Never print
// input, errors, tokens, DSNs, cookies or objects. The caller owns the migrated
// isolated PostgreSQL database, real auth server/JWT and retained evidence.
// Compile host AND test executable before minting the short-lived real JWT;
// invoke the executable once with -test.run '^TestOrdersBrowserSeed$'
// -test.count=1 -test.timeout=120s. No migrations, provisioning or JWT signing
// occur here. Only owned pools and fake HTTP resources close on return.
func TestOrdersBrowserSeed(t *testing.T) {
	inputPath, present := os.LookupEnv("STAFF_ORDERS_BROWSER_SEED_INPUT")
	if !present {
		t.Skip("browser seed input absent; opt-in isolated fixture only")
	}
	check := func(ok bool, stage string) {
		t.Helper()
		if !ok {
			t.Fatal(stage)
		}
	}
	outputPath := os.Getenv("STAFF_ORDERS_BROWSER_SEED_OUTPUT")
	root := filepath.Dir(inputPath)
	check(filepath.IsAbs(inputPath) && filepath.Clean(inputPath) == inputPath && filepath.Dir(root) == "/tmp" && root != "/tmp", "seed input path rejected")
	check(filepath.IsAbs(outputPath) && filepath.Clean(outputPath) == outputPath && filepath.Dir(outputPath) == root && outputPath != inputPath, "seed output path rejected")
	rootInfo, err := os.Lstat(root)
	check(err == nil && browserSeedPrivate(rootInfo, 0700, true), "seed evidence directory rejected")
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	check(err == nil, "seed evidence directory open failed")
	dir := os.NewFile(uintptr(fd), "seed evidence directory")
	defer dir.Close()
	openedRoot, err := dir.Stat()
	check(err == nil && os.SameFile(rootInfo, openedRoot), "seed evidence directory changed")
	_, err = os.Lstat(outputPath)
	check(os.IsNotExist(err), "seed output already exists or unavailable")
	inputFD, err := syscall.Openat(fd, filepath.Base(inputPath), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	check(err == nil, "seed input open failed")
	input := os.NewFile(uintptr(inputFD), "seed input")
	defer input.Close()
	info, err := input.Stat()
	check(err == nil && browserSeedPrivate(info, 0600, false) && info.Size() > 0 && info.Size() <= 32768, "seed input metadata rejected")
	body, err := io.ReadAll(io.LimitReader(input, 32769))
	check(err == nil && len(body) <= 32768 && utf8.Valid(body), "seed input read failed")
	fields := map[string]string{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	check(err == nil && first == json.Delim('{'), "seed input JSON rejected")
	allowed := map[string]bool{"database_url": true, "restaurant_id": true, "subject": true, "issuer": true, "jwks_url": true, "token": true, "evidence_dir": true}
	for decoder.More() {
		key, e := decoder.Token()
		name, ok := key.(string)
		check(e == nil && ok && allowed[name], "seed input field rejected")
		_, duplicate := fields[name]
		check(!duplicate, "seed duplicate input field rejected")
		var value string
		err = decoder.Decode(&value)
		check(err == nil && strings.TrimSpace(value) != "" && !strings.ContainsRune(value, 0), "seed input value rejected")
		fields[name] = value
	}
	last, err := decoder.Token()
	check(err == nil && last == json.Delim('}') && len(fields) == len(allowed), "seed input fields incomplete")
	_, err = decoder.Token()
	check(err == io.EOF && fields["evidence_dir"] == root, "seed input trailing data or directory mismatch")

	// pgx consults ambient PG settings and passfiles even during ParseConfig.
	// Reject ambient PG configuration before parsing, then explicitly select the
	// empty system device, never HOME/.pgpass or a service/credential file.
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") {
			check((name == "PGPASSFILE" || name == "PGSERVICEFILE") && value == "/dev/null", "seed ambient PostgreSQL configuration rejected")
		}
	}
	t.Setenv("PGPASSFILE", "/dev/null")
	t.Setenv("PGSERVICEFILE", "/dev/null")
	dsn := fields["database_url"]
	u, err := url.Parse(dsn)
	check(err == nil && u != nil, "seed database URL rejected")
	check((u.Scheme == "postgres" || u.Scheme == "postgresql") && u.Opaque == "" && u.Fragment == "" && u.RawFragment == "" && u.RawPath == "" && !u.ForceQuery && u.User != nil, "seed database URL shape rejected")
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	check(ip != nil && ip.IsLoopback() && err == nil && port > 0 && port <= 65535 && u.Host == net.JoinHostPort(u.Hostname(), strconv.Itoa(port)), "seed database endpoint rejected")
	check(regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(u.User.Username()) && regexp.MustCompile(`^/restaurant_foundation_[A-Za-z0-9_]+$`).MatchString(u.Path), "seed database identity rejected")
	if password, has := u.User.Password(); has {
		check(password != "" && !strings.ContainsAny(password, "\x00\r\n"), "seed database password shape rejected")
	}
	check(u.RawQuery == "" || u.RawQuery == "sslmode=disable", "seed database options rejected")
	u.RawQuery = "sslmode=disable"
	dsn = u.String()
	cfg, err := postgres.LocalPoolConfig(dsn)
	check(err == nil, "seed guarded database configuration rejected")
	check(cfg.ConnConfig.Host == u.Hostname() && cfg.ConnConfig.Port == uint16(port) && cfg.ConnConfig.Database == strings.TrimPrefix(u.Path, "/") && cfg.ConnConfig.User == u.User.Username() && cfg.ConnConfig.TLSConfig == nil && len(cfg.ConnConfig.Fallbacks) == 0, "seed database configuration mismatch")

	ctx := t.Context()
	members, err := staff.OpenStore(ctx, dsn, fields["restaurant_id"])
	check(err == nil, "seed staff store open failed")
	defer members.Close()
	verifier, err := staff.NewVerifier(fields["issuer"], fields["jwks_url"])
	check(err == nil, "seed verifier configuration rejected")
	subject, err := verifier.Verify(ctx, fields["token"])
	check(err == nil && subject == fields["subject"], "seed real identity verification failed")
	active, err := members.EnsureMember(ctx, subject)
	check(err == nil && active, "seed membership inactive or unavailable")
	active, err = members.Access(ctx, subject)
	check(err == nil && active, "seed membership authorization failed")
	cat, err := catalog.OpenStore(ctx, dsn, fields["restaurant_id"])
	check(err == nil, "seed catalog open failed")
	defer cat.Close()
	const menuID = "10000000-0000-4000-8000-000000000001"
	err = cat.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true}, []catalog.Item{{ID: menuID, Name: "Fictional sandwich", PriceMinor: 650, Currency: "USD", Available: true}})
	check(err == nil, "seed catalog initialization failed")
	s, err := cart.OpenStore(ctx, dsn, fields["restaurant_id"])
	check(err == nil, "seed cart open failed")
	defer s.Close()
	local, err := s.ProvisionLocal(ctx, "browser-seed-business", "browser-seed-sender")
	check(err == nil, "seed conversation provisioning failed")
	control, err := conversation.OpenStore(ctx, dsn, fields["restaurant_id"], verifier)
	check(err == nil, "seed conversation store open failed")
	defer control.Close()
	_, err = control.Initialize(ctx, fields["token"], local.ConversationID(), conversation.ModeAI, "browser-seed-init")
	check(err == nil, "seed real authenticated conversation initialization failed")
	op, err := cart.NewOperationID()
	check(err == nil, "seed create operation failed")
	created, err := s.Create(ctx, local, op)
	check(err == nil, "seed cart creation failed")
	menu, err := cart.ParseMenuID(menuID)
	check(err == nil, "seed menu identity rejected")
	add, err := cart.Add(menu, 2)
	check(err == nil, "seed add command rejected")
	op, err = cart.NewOperationID()
	check(err == nil, "seed add operation failed")
	added, err := s.Apply(ctx, local, op, created.Cart.ID, created.Cart.Version, add)
	check(err == nil && len(added.Cart.Units) == 2, "seed two-unit add failed")
	note, err := cart.SetNote([]cart.UnitID{added.Cart.Units[0].ID}, "  no salt 🥪\n")
	check(err == nil, "seed note command rejected")
	op, err = cart.NewOperationID()
	check(err == nil, "seed note operation failed")
	noted, err := s.Apply(ctx, local, op, added.Cart.ID, added.Cart.Version, note)
	check(err == nil, "seed note application failed")
	jobs, err := postgres.Open(ctx, dsn)
	check(err == nil, "seed job store open failed")
	defer jobs.Close()
	now := time.Now()
	event := foundation.Event{ID: "browser-seed-fulfillment", Sender: "browser-seed-sender", Text: "select pickup"}
	_, err = jobs.Accept(ctx, event, now)
	check(err == nil, "seed fulfillment admission failed")
	job, ok, err := jobs.ClaimJob(ctx, now, time.Minute)
	check(err == nil && ok && job.Event.ID == event.ID, "seed owned fulfillment claim failed")
	call, err := cart.SetFulfillmentCall(noted.Cart.ID, noted.Cart.Version, cart.PickupFulfillment())
	check(err == nil, "seed fulfillment call rejected")
	plan, err := s.RegisterExecution(ctx, local, event.ID, 1, call)
	check(err == nil, "seed fulfillment registration failed")
	selected, err := s.ExecuteDraft(ctx, local, plan, job)
	check(err == nil, "seed fulfillment execution failed")
	err = jobs.CompleteJob(ctx, job, "fixture fulfillment selected", time.Now())
	check(err == nil, "seed fulfillment completion failed")
	pickup, err := cart.NewPickupStore(s, fields["restaurant_id"], cart.PickupCompletionAssessmentVersion)
	check(err == nil, "seed pickup store construction failed")

	var assessments, gates atomic.Int64
	model := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var wire struct {
			Tools []struct {
				Function commandcode.ToolDefinition `json:"function"`
			} `json:"tools"`
		}
		if r.Method != "POST" || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&wire) != nil || len(wire.Tools) != 1 {
			t.Error("seed model request rejected")
			http.Error(w, "invalid request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch wire.Tools[0].Function.Name {
		case "get_menu":
			gates.Add(1)
			io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"Local placeholder, not presented"},"finish_reason":"stop"}]}`)
		case "assess_pickup_completion":
			assessments.Add(1)
			io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"browser-seed-assessment","type":"function","function":{"name":"assess_pickup_completion","arguments":"{\"decision\":\"complete\"}"}}]}}]}`)
		default:
			t.Error("seed model tool rejected")
			http.Error(w, "unexpected tool", 400)
		}
	}))
	model.Config.ReadHeaderTimeout = 5 * time.Second
	model.Config.ReadTimeout = 5 * time.Second
	model.Config.WriteTimeout = 5 * time.Second
	model.Start()
	defer model.Close()
	destination, err := url.Parse(model.URL)
	check(err == nil, "seed fake provider URL rejected")
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	client, err := commandcode.New(commandcode.Config{APIKey: "synthetic-browser-seed-key", Transport: connectedTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != commandcode.Endpoint {
			return nil, errors.New("seed model endpoint rejected")
		}
		owned := r.Clone(r.Context())
		owned.URL = destination
		owned.Host = destination.Host
		return transport.RoundTrip(owned)
	})})
	check(err == nil, "seed model client construction failed")
	gate, err := menugate.Open(ctx, dsn, fields["restaurant_id"], client, 100)
	check(err == nil, "seed gate open failed")
	defer gate.Close()
	inbound := func(id, text string) cart.PickupExecution {
		_, fresh, e := s.AcceptMenu(ctx, local, foundation.Event{ID: id, Sender: event.Sender, Text: text}, time.Now())
		check(e == nil && fresh, "seed menu admission failed")
		claim, owned, e := s.ClaimMenu(ctx, local, time.Minute)
		check(e == nil && owned, "seed owned menu claim failed")
		zero, e := cart.ParseVersion(0)
		check(e == nil, "seed control version rejected")
		return cart.PickupExecution{Local: local, Source: claim.Source(), Claim: claim.Job(), ControlVersion: zero}
	}
	x := inbound("browser-seed-summary", "Show the final pickup summary")
	call, err = cart.PreparePickupCall(selected.Cart.ID, selected.Cart.Version)
	check(err == nil, "seed summary call rejected")
	x.Execution, err = s.RegisterExecution(ctx, local, x.Source.EventProviderID(), 1, call)
	check(err == nil, "seed summary registration failed")
	summary, err := pickup.Prepare(ctx, x, selected.Cart.ID, selected.Cart.Version)
	check(err == nil, "seed pickup summary preparation failed")
	queued, err := pickup.ReadLocalIntents(ctx, local, cart.PickupIntentID{}, 100)
	check(err == nil && len(queued) == 1 && queued[0].Kind == "summary" && queued[0].ID == summary.Intent.ID && queued[0].CandidateID == summary.CandidateID && queued[0].Text == summary.Intent.Text, "seed authoritative summary intent missing")
	receiver := testpickupreceiver.New(t)
	payload, err := json.Marshal(testpickupreceiver.Message{CorrelationID: queued[0].ID.String(), Text: queued[0].Text})
	check(err == nil, "seed presentation encoding failed")
	req, err := http.NewRequestWithContext(ctx, "POST", receiver.URL(), bytes.NewReader(payload))
	check(err == nil, "seed presentation request failed")
	req.Header.Set("Content-Type", "application/json")
	res, err := receiver.Client().Do(req)
	check(err == nil, "seed actual HTTP presentation failed")
	var ack testpickupreceiver.Acknowledgement
	err = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&ack)
	res.Body.Close()
	check(err == nil, "seed presentation acknowledgement decoding failed")
	digest, err := hex.DecodeString(ack.ContentSHA256)
	check(err == nil && res.StatusCode == 200 && ack.CorrelationID == queued[0].ID.String() && ack.ReceiverReference != "" && len(digest) == sha256.Size && bytes.Equal(digest, summary.Intent.TextDigest[:]), "seed actual HTTP acknowledgement rejected")
	var receivedDigest [32]byte
	copy(receivedDigest[:], digest)
	_, err = pickup.RecordLocalPresentation(ctx, local, cart.PickupLocalPresentation{IntentID: queued[0].ID, CandidateID: queued[0].CandidateID, ReceiverReference: ack.ReceiverReference, ReceivedTextDigest: receivedDigest})
	check(err == nil, "seed local presentation recording failed")
	_, err = gate.RunAndQueue(ctx, x.Source, x.Claim)
	check(err == nil, "seed real gate execution failed")
	x = inbound("browser-seed-confirmation", "No, that's all. Place the order.")
	assessor, err := pickupintent.New(pickupintent.Config{Client: client, MaxOutputTokens: 80})
	check(err == nil, "seed assessor construction failed")
	submitted, err := pickup.AssessAndSubmit(ctx, x, 1, assessor)
	check(err == nil, "seed actual assessment and submission failed")
	order := submitted.Order
	check(assessments.Load() == 1 && gates.Load() == 1 && order.Status == cart.PickupSubmitted && order.Version.Int64() == 0 && order.Confirmed.ID == summary.CandidateID && order.Confirmed.UnitCount == 2 && order.Total.AmountMinor == 1300 && order.Payment.State == cart.PickupUnpaid && order.Payment.CollectedAt == nil, "seed submitted order invariant failed")
	persisted, err := pickup.ReadOrder(ctx, cart.PickupStaffSubject(subject), order.ID)
	check(err == nil && reflect.DeepEqual(persisted, order), "seed authorized persisted detail mismatch")
	orders, err := pickup.ListOrders(ctx, cart.PickupStaffSubject(subject), cart.PickupPageRequest{})
	check(err == nil && len(orders.Items) == 1 && orders.Items[0].ID == order.ID && orders.NextCursor == nil, "seed authorized persisted list mismatch")
	units, err := pickup.ReadUnits(ctx, cart.PickupStaffSubject(subject), order.ID, order.Confirmed.ID, cart.PickupPageRequest{})
	check(err == nil && units.SummaryID == order.Confirmed.ID && units.UnitCount == 2 && len(units.Items) == 2 && units.NextCursor == nil, "seed authorized immutable unit read failed")
	for i, unit := range units.Items {
		check(unit.ID == selected.Cart.Units[i].ID && unit.MenuID == menu && unit.Name == "Fictional sandwich" && unit.Note == []string{"  no salt 🥪\n", ""}[i] && unit.Price.AmountMinor == 650 && unit.Price.Currency == "USD", "seed immutable unit content mismatch")
	}
	check(units.Items[0].ID != units.Items[1].ID && !t.Failed(), "seed immutable unit identity or prior stage failed")
	// Recheck the named directory before publishing, and create via its pinned
	// descriptor: an intervening symlink can never redirect the private output.
	currentRoot, err := os.Lstat(root)
	check(err == nil && browserSeedPrivate(currentRoot, 0700, true) && os.SameFile(openedRoot, currentRoot), "seed evidence directory changed before output")
	outputFD, err := syscall.Openat(fd, filepath.Base(outputPath), syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	check(err == nil, "seed exclusive output creation failed")
	output := os.NewFile(uintptr(outputFD), "seed output")
	defer output.Close()
	outInfo, err := output.Stat()
	check(err == nil && browserSeedPrivate(outInfo, 0600, false), "seed output metadata rejected")
	result := struct {
		OrderID        string `json:"order_id"`
		SummaryID      string `json:"summary_id"`
		InitialVersion string `json:"initial_version"`
		UnitCount      int    `json:"unit_count"`
	}{order.ID.String(), order.Confirmed.ID.String(), strconv.FormatInt(order.Version.Int64(), 10), units.UnitCount}
	err = json.NewEncoder(output).Encode(result)
	check(err == nil, "seed output write failed")
	err = output.Sync()
	check(err == nil, "seed output sync failed")
	err = output.Close()
	check(err == nil, "seed output close failed")
}

func browserSeedPrivate(info os.FileInfo, mode os.FileMode, directory bool) bool {
	if info == nil || info.Mode().Perm() != mode || info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && (directory || stat.Nlink == 1)
}
