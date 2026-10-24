package catalog_test

import (
	"fmt"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

func TestPageTraversal(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	s := open(t, dsn, restaurant)
	cfg, _ := fixture()
	items := make([]catalog.Item, 105)
	for i := range items {
		items[i] = catalog.Item{ID: fmt.Sprintf("10000000-0000-4000-8000-%012d", i+1), Name: "料理 <&>", PriceMinor: int64(i), Currency: "USD", Available: true}
	}
	if err := s.Initialize(t.Context(), cfg, items); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, 50, 100} {
		var after *catalog.Position
		var got []catalog.Item
		for {
			p, err := s.Page(t.Context(), limit, after)
			if err != nil || p.Settings != cfg || len(p.Items) > limit {
				t.Fatalf("page %+v %v", p, err)
			}
			got = append(got, p.Items...)
			if p.Next == nil {
				break
			}
			if len(p.Items) == 0 {
				t.Fatal("nonadvancing page")
			}
			after = p.Next
		}
		if !reflect.DeepEqual(got, items) {
			t.Fatalf("traversal limit %d: %d items", limit, len(got))
		}
	}
	for _, limit := range []int{0, 101} {
		if _, err := s.Page(t.Context(), limit, nil); err != catalog.ErrInput {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	equalState(t, s, cfg, items)
}
