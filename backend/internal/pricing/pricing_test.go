package pricing_test

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"restaurant-assistant/backend/internal/pricing"
)

const (
	sandwich = "10000000-0000-0000-0000-000000000001"
	water = "10000000-0000-0000-0000-000000000002"
	u1 = "20000000-0000-0000-0000-000000000001"
	u2 = "20000000-0000-0000-0000-000000000002"
	u3 = "20000000-0000-0000-0000-000000000003"
)

func fixture() (pricing.Settings, []pricing.Item) {
	return pricing.Settings{Currency: "USD", Exponent: 2, DeliveryFee: 300}, []pricing.Item{
		{ID: sandwich, Name: "Garden Sandwich", Currency: "USD", Price: 650, Available: true},
		{ID: water, Name: "Sparkling Water", Currency: "USD", Price: 200, Available: true},
	}
}

func engine(t *testing.T) *pricing.Engine {
	t.Helper()
	s, c := fixture()
	e, err := pricing.New(s, c)
	if err != nil { t.Fatal(err) }
	return e
}

func same(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) { t.Fatalf("got %#v; want %#v", got, want) }
}

func TestCanonicalDraft(t *testing.T) {
	e := engine(t)
	var draft []pricing.Unit
	for _, u := range []pricing.Unit{{ID: u1, MenuID: sandwich, Note: "no onions"}, {ID: u2, MenuID: sandwich}, {ID: u3, MenuID: water}} {
		before := append([]pricing.Unit(nil), draft...)
		next, err := e.Add(draft, u)
		if err != nil { t.Fatal(err) }
		same(t, draft, before)
		draft = next
	}
	want := []pricing.Unit{{ID: u1, MenuID: sandwich, Note: "no onions"}, {ID: u2, MenuID: sandwich}, {ID: u3, MenuID: water}}
	same(t, draft, want)
	for _, tc := range []struct { name string; fulfillment pricing.Fulfillment; fee, total int64; payment string }{
		{"pickup", pricing.Fulfillment{Kind: pricing.Pickup}, 0, 1500, "pay_at_pickup"},
		{"delivery", pricing.Fulfillment{Kind: pricing.Delivery, Address: "10 Fictional Street", Phone: "555-0100"}, 300, 1800, "cash_on_delivery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := e.Quote(draft, tc.fulfillment)
			if err != nil { t.Fatal(err) }
			same(t, q, pricing.Quote{Units: want, Currency: "USD", Exponent: 2, Subtotal: 1500, Fee: tc.fee, Total: tc.total, Payment: tc.payment, Fulfillment: tc.fulfillment})
			q.Units[0].Note = "changed"
			same(t, draft, want)
		})
	}
	remaining, err := e.Remove(draft, u2)
	if err != nil { t.Fatal(err) }
	same(t, remaining, []pricing.Unit{want[0], want[2]})
	q, err := e.Quote(remaining, pricing.Fulfillment{Kind: pricing.Pickup})
	if err != nil { t.Fatal(err) }
	same(t, q, pricing.Quote{Units: remaining, Currency: "USD", Exponent: 2, Subtotal: 850, Total: 850, Payment: "pay_at_pickup", Fulfillment: pricing.Fulfillment{Kind: pricing.Pickup}})
	remaining[0].Note = "changed"
	same(t, draft, want)
	noted, err := e.SetNote(draft, u1, "  exact 🥪\n")
	if err != nil { t.Fatal(err) }
	wantNote := append([]pricing.Unit(nil), want...)
	wantNote[0].Note = "  exact 🥪\n"
	same(t, noted, wantNote)
	noted[1].Note = "changed"
	same(t, draft, want)
}

func TestSettingsAndCatalogValidation(t *testing.T) {
	for _, tc := range []struct { name string; change func(*pricing.Settings, *[]pricing.Item); message string }{
		{"currency", func(s *pricing.Settings, _ *[]pricing.Item) { s.Currency = "usd" }, "invalid settings"},
		{"ascii", func(s *pricing.Settings, _ *[]pricing.Item) { s.Currency = "ÜSD" }, "invalid settings"},
		{"exponent negative", func(s *pricing.Settings, _ *[]pricing.Item) { s.Exponent = -1 }, "invalid settings"},
		{"exponent high", func(s *pricing.Settings, _ *[]pricing.Item) { s.Exponent = 7 }, "invalid settings"},
		{"fee", func(s *pricing.Settings, _ *[]pricing.Item) { s.DeliveryFee = -1 }, "invalid settings"},
		{"price", func(_ *pricing.Settings, c *[]pricing.Item) { (*c)[0].Price = -1 }, "invalid catalog"},
		{"mismatch", func(_ *pricing.Settings, c *[]pricing.Item) { (*c)[0].Currency = "EUR" }, "invalid catalog"},
		{"id", func(_ *pricing.Settings, c *[]pricing.Item) { (*c)[0].ID = "bad" }, "invalid catalog"},
		{"uppercase id", func(_ *pricing.Settings, c *[]pricing.Item) { (*c)[0].ID = "ABCDEF00-0000-0000-0000-000000000001" }, "invalid catalog"},
		{"duplicate", func(_ *pricing.Settings, c *[]pricing.Item) { *c = append(*c, (*c)[0]) }, "invalid catalog"},
		{"blank name", func(_ *pricing.Settings, c *[]pricing.Item) { (*c)[0].Name = " \t" }, "invalid catalog"},
		{"long name", func(_ *pricing.Settings, c *[]pricing.Item) { (*c)[0].Name = strings.Repeat("🥪", 201) }, "invalid catalog"},
		{"utf8 name", func(_ *pricing.Settings, c *[]pricing.Item) { (*c)[0].Name = "\xff" }, "invalid catalog"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := fixture(); tc.change(&s, &c)
			before := append([]pricing.Item(nil), c...); original := s
			e, err := pricing.New(s, c)
			if err == nil || err.Error() != tc.message || e != nil { t.Fatalf("engine=%v error=%v; want %q", e, err, tc.message) }
			same(t, c, before); same(t, s, original)
		})
	}
	for _, exponent := range []int{0, 6} {
		s, c := fixture(); s.Exponent = exponent; s.DeliveryFee = 0; c[0].Price = 0; c[0].Name = strings.Repeat("🥪", 200)
		if _, err := pricing.New(s, c); err != nil { t.Fatal(err) }
	}
}

func TestRejectedOperationsPreserveDraft(t *testing.T) {
	e := engine(t)
	base := []pricing.Unit{{ID: u1, MenuID: sandwich, Note: "original"}}
	for _, tc := range []struct { name string; run func([]pricing.Unit) ([]pricing.Unit, error); message string }{
		{"duplicate add", func(d []pricing.Unit) ([]pricing.Unit,error) { return e.Add(d, d[0]) }, "duplicate unit ID"},
		{"unknown add", func(d []pricing.Unit) ([]pricing.Unit,error) { return e.Add(d, pricing.Unit{ID:u2, MenuID:u3}) }, "unknown menu item"},
		{"invalid id", func(d []pricing.Unit) ([]pricing.Unit,error) { return e.Add(d, pricing.Unit{ID:"bad", MenuID:water}) }, "invalid unit"},
		{"missing remove", func(d []pricing.Unit) ([]pricing.Unit,error) { return e.Remove(d,u2) }, "unit not found"},
		{"missing note", func(d []pricing.Unit) ([]pricing.Unit,error) { return e.SetNote(d,u2,"x") }, "unit not found"},
		{"long note", func(d []pricing.Unit) ([]pricing.Unit,error) { return e.SetNote(d,u1,strings.Repeat("🥪",501)) }, "invalid note"},
		{"utf8 note", func(d []pricing.Unit) ([]pricing.Unit,error) { return e.SetNote(d,u1,"\xff") }, "invalid note"},
		{"long add note", func(d []pricing.Unit) ([]pricing.Unit,error) { return e.Add(d,pricing.Unit{ID:u2,MenuID:water,Note:strings.Repeat("x",501)}) }, "invalid unit"},
	} {
		t.Run(tc.name,func(t *testing.T) {
			d := append([]pricing.Unit(nil),base...)
			got,err := tc.run(d)
			if err == nil || err.Error()!=tc.message || got!=nil { t.Fatalf("got=%v error=%v; want %q",got,err,tc.message) }
			same(t,d,base)
		})
	}
	next,err := e.SetNote(base,u1,strings.Repeat("🥪",500)); if err!=nil { t.Fatal(err) }; same(t,next,[]pricing.Unit{{ID:u1,MenuID:sandwich,Note:strings.Repeat("🥪",500)}})
	for _, tc := range []struct { draft []pricing.Unit; message string }{
		{[]pricing.Unit{{ID:u1,MenuID:sandwich},{ID:u1,MenuID:water}}, "duplicate unit ID"},
		{[]pricing.Unit{{ID:"",MenuID:sandwich}}, "invalid unit"},
		{[]pricing.Unit{{ID:u1,MenuID:u2}}, "unknown menu item"},
		{[]pricing.Unit{{ID:u1,MenuID:water,Note:"\xff"}}, "invalid unit"},
	} {
		d := tc.draft
		before:=append([]pricing.Unit(nil),d...)
		for _, run := range []func() error{
			func() error { _,err:=e.Add(d,pricing.Unit{ID:u3,MenuID:water}); return err },
			func() error { _,err:=e.Remove(d,u1); return err },
			func() error { _,err:=e.SetNote(d,u1,"x"); return err },
			func() error { q,err:=e.Quote(d,pricing.Fulfillment{Kind:pricing.Pickup}); same(t,q,pricing.Quote{}); return err },
		} { if err:=run(); err==nil || err.Error()!=tc.message { t.Fatalf("error=%v; want %q",err,tc.message) }; same(t,d,before) }
	}
}

func TestFulfillmentAndEmptyDraft(t *testing.T) {
	e:=engine(t)
	for _, f:= range []pricing.Fulfillment{
		{}, {Kind:"unknown"}, {Kind:pricing.Pickup,Address:"x"}, {Kind:pricing.Pickup,Phone:"x"},
		{Kind:pricing.Delivery,Address:" ",Phone:"x"}, {Kind:pricing.Delivery,Address:"x",Phone:" "},
		{Kind:pricing.Delivery,Address:strings.Repeat("a",1001),Phone:"x"}, {Kind:pricing.Delivery,Address:"x",Phone:strings.Repeat("a",129)},
		{Kind:pricing.Delivery,Address:"\xff",Phone:"x"}, {Kind:pricing.Delivery,Address:"x",Phone:"\xff"},
	} {
		q,err:=e.Quote(nil,f)
		if err==nil || err.Error()!="invalid fulfillment" { t.Fatalf("error=%v",err) }; same(t,q,pricing.Quote{})
	}
	for _, f:=range []pricing.Fulfillment{{Kind:pricing.Pickup},{Kind:pricing.Delivery,Address:strings.Repeat("🥪",1000),Phone:strings.Repeat("☎",128)}} {
		q,err:=e.Quote(nil,f); if err!=nil { t.Fatal(err) }
		fee:=int64(0); payment:="pay_at_pickup"; if f.Kind==pricing.Delivery { fee=300; payment="cash_on_delivery" }
		same(t,q,pricing.Quote{Currency:"USD",Exponent:2,Fee:fee,Total:fee,Payment:payment,Fulfillment:f})
	}
}

func TestNoOrderSizeCap(t *testing.T) {
	e := engine(t)
	draft := make([]pricing.Unit, 10001)
	for i := range draft {
		draft[i] = pricing.Unit{ID: fmt.Sprintf("30000000-0000-0000-0000-%012x", i), MenuID: water}
	}
	before := append([]pricing.Unit(nil), draft...)
	next, err := e.Add(draft, pricing.Unit{ID:u1, MenuID:water})
	if err != nil { t.Fatal(err) }
	same(t, next, append(append([]pricing.Unit(nil), before...), pricing.Unit{ID:u1, MenuID:water}))
	q, err := e.Quote(next, pricing.Fulfillment{Kind:pricing.Pickup})
	if err != nil { t.Fatal(err) }
	same(t, q, pricing.Quote{Units:next, Currency:"USD", Exponent:2, Subtotal:2000400, Total:2000400, Payment:"pay_at_pickup", Fulfillment:pricing.Fulfillment{Kind:pricing.Pickup}})
	same(t, draft, before)
}

func TestAvailabilitySnapshotAndOverflow(t *testing.T) {
	s,c:=fixture(); c[0].Available=false
	e,err:=pricing.New(s,c); if err!=nil { t.Fatal(err) }
	c[0].Available=true; c[0].Price=1
	d:=[]pricing.Unit{{ID:u1,MenuID:sandwich,Note:"retained"}}
	got,err:=e.Add(d,pricing.Unit{ID:u2,MenuID:sandwich})
	if err==nil || err.Error()!="menu item unavailable" || got!=nil { t.Fatalf("got=%v error=%v",got,err) }
	q,err:=e.Quote(d,pricing.Fulfillment{Kind:pricing.Pickup}); if err!=nil { t.Fatal(err) }
	same(t,q,pricing.Quote{Units:d,Currency:"USD",Exponent:2,Subtotal:650,Total:650,Payment:"pay_at_pickup",Fulfillment:pricing.Fulfillment{Kind:pricing.Pickup}})
	s,c=fixture(); c[0].Price=math.MaxInt64; c[1].Price=0
	e,err=pricing.New(s,c); if err!=nil { t.Fatal(err) }
	d=[]pricing.Unit{{ID:u1,MenuID:sandwich}}
	q,err=e.Quote(d,pricing.Fulfillment{Kind:pricing.Pickup}); if err!=nil || q.Total!=math.MaxInt64 { t.Fatalf("quote=%v error=%v",q,err) }
	for _, f:=range []pricing.Fulfillment{{Kind:pricing.Pickup},{Kind:pricing.Delivery,Address:"x",Phone:"y"}} {
		input:=d; if f.Kind==pricing.Pickup { input=append(append([]pricing.Unit(nil),d...),pricing.Unit{ID:u2,MenuID:sandwich}) }
		before:=append([]pricing.Unit(nil),input...)
		q,err=e.Quote(input,f); if err==nil || err.Error()!="amount overflow" { t.Fatalf("quote=%v error=%v",q,err) }; same(t,q,pricing.Quote{}); same(t,input,before)
	}
	got,err=e.Add(d,pricing.Unit{ID:u2,MenuID:sandwich})
	if err==nil || err.Error()!="amount overflow" || got!=nil { t.Fatalf("got=%v error=%v",got,err) }
	same(t,d,[]pricing.Unit{{ID:u1,MenuID:sandwich}})
}
