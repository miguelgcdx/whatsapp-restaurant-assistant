// This explicit test-only command configures fictional data in the harness-owned DB.
// Runtime, migrations and setup never invoke it.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"restaurant-assistant/backend/internal/catalog"
)

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := catalog.OpenStore(ctx, os.Getenv("LOCAL_DATABASE_URL"), os.Getenv("LOCAL_RESTAURANT_ID"))
	if err != nil {
		return err
	}
	defer store.Close()
	return store.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true}, []catalog.Item{
		{ID: "00000000-0000-4000-8000-000000000001", Name: "Fictional bowl", PriceMinor: 650, Currency: "USD", Available: true},
		{ID: "00000000-0000-4000-8000-000000000002", Name: "Fictional soup", PriceMinor: 450, Currency: "USD", Available: false},
		{ID: "00000000-0000-4000-8000-000000000003", Name: "Fictional tea", PriceMinor: 200, Currency: "USD", Available: true},
	})
}

func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "Explicit browser catalog fixture failed.")
		os.Exit(1)
	}
}
