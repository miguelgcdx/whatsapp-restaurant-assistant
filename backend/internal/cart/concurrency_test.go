package cart_test

import (
	"reflect"
	"sync"
	"testing"
)

func TestDistinctCreationIdentitiesConvergeOnOneDraft(t *testing.T) {
	f := setup(t)
	a, b := operation(t), operation(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	type outcome struct {
		cart    string
		version int64
		err     error
	}
	out := make(chan outcome, 2)
	for _, op := range []struct{ index int }{{0}, {1}} {
		wg.Go(func() {
			<-start
			id := a
			if op.index == 1 {
				id = b
			}
			r, err := f.s.Create(t.Context(), f.local, id)
			out <- outcome{r.Cart.ID.String(), r.Cart.Version.Int64(), err}
		})
	}
	close(start)
	wg.Wait()
	first, second := <-out, <-out
	if first.err != nil || second.err != nil || first.cart == "" || !reflect.DeepEqual(first, second) || first.version != 0 {
		t.Fatalf("distinct creations=%+v %+v", first, second)
	}
}
