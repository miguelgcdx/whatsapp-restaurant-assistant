package menuengine_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/catalog"
	cc "restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/menuengine"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

func zeroError(t *testing.T, got menuengine.Result, err, want error) {
	t.Helper()
	if !reflect.DeepEqual(got, menuengine.Result{}) || !errors.Is(err, want) {
		t.Fatalf("want zero / %v; got %+v / %v", want, got, err)
	}
	if err != nil && (strings.Contains(err.Error(), privateText) || strings.Contains(err.Error(), fakeKey)) {
		t.Fatal("sensitive fixture leaked")
	}
}
func TestMenuTurnInputs(t *testing.T) {
	c, tr := testClient(t, func(int, wireRequest) (*http.Response, error) {
		t.Error("invalid input reached HTTP")
		return answer(), nil
	})
	reads := 0
	read := func(context.Context) (catalog.MenuSnapshot, error) { reads++; return menuFixture(), nil }
	for _, cfg := range []menuengine.Config{
		{ReadMenu: read, MaxOutputTokens: 321}, {Client: c, MaxOutputTokens: 321},
		{Client: c, ReadMenu: read, MaxOutputTokens: 0}, {Client: c, ReadMenu: read, MaxOutputTokens: 8193},
	} {
		if e, err := menuengine.New(cfg); e != nil || !errors.Is(err, menuengine.ErrInput) {
			t.Fatalf("invalid config %v", err)
		}
	}
	for _, tokens := range []int{1, 8192} {
		if _, err := menuengine.New(menuengine.Config{Client: c, ReadMenu: read, MaxOutputTokens: tokens}); err != nil {
			t.Fatal(err)
		}
	}
	e := newEngine(t, c, read)
	for _, text := range []string{"", " \t\n", string([]byte{255}), "text\x00"} {
		got, err := e.Run(t.Context(), text)
		zeroError(t, got, err, menuengine.ErrInput)
	}
	got, err := e.Run(nil, "menu")
	zeroError(t, got, err, menuengine.ErrInput)
	var nilEngine *menuengine.Engine
	got, err = nilEngine.Run(t.Context(), "menu")
	zeroError(t, got, err, menuengine.ErrInput)
	got, err = new(menuengine.Engine).Run(t.Context(), "menu")
	zeroError(t, got, err, menuengine.ErrInput)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err = e.Run(ctx, "menu")
	zeroError(t, got, err, context.Canceled)
	ctx, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	got, err = e.Run(ctx, "menu")
	zeroError(t, got, err, context.DeadlineExceeded)
	if tr.count() != 0 || reads != 0 {
		t.Fatal("invalid inputs caused effects")
	}
	for err, want := range map[error]string{menuengine.ErrInput: "invalid menu turn input", menuengine.ErrProtocol: "invalid menu turn response", menuengine.ErrMenuUnavailable: "menu temporarily unavailable", menuengine.ErrResource: "menu turn exceeds local budget"} {
		if err.Error() != want {
			t.Fatalf("unstable public error %v", err)
		}
	}
}

func TestMenuTurnDirectAndIsolatedRuns(t *testing.T) {
	c, tr := testClient(t, func(_ int, w wireRequest) (*http.Response, error) {
		if len(w.Messages) != 2 || w.Messages[0].Role != cc.RoleSystem || w.Messages[1].Role != cc.RoleUser {
			t.Error("history crossed runs")
		}
		return answer(), nil
	})
	reads := 0
	e := newEngine(t, c, func(context.Context) (catalog.MenuSnapshot, error) { reads++; return menuFixture(), nil })
	for _, text := range []string{"first", "second"} {
		got, err := e.Run(t.Context(), text)
		if err != nil || got.Reply != "menu proposal" || got.Menu != nil {
			t.Fatalf("direct %+v %v", got, err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := e.Run(t.Context(), "concurrent")
			if err != nil || got.Menu != nil || got.Reply != "menu proposal" {
				t.Error("concurrent result")
			}
		}()
	}
	wg.Wait()
	if tr.count() != 4 || reads != 0 {
		t.Fatal("direct path read a menu or repeated a request")
	}
}

func TestMenuTurnDispatchBeforeLookup(t *testing.T) {
	cases := []struct {
		name  string
		calls []cc.ToolCall
		want  error
	}{
		{"multiple", []cc.ToolCall{call(), {ID: "other_call", Type: "function", Function: cc.FunctionCall{Name: "get_menu", Arguments: "{}"}}}, menuengine.ErrProtocol},
		{"duplicate ids", []cc.ToolCall{call(), call()}, cc.ErrProtocol},
	}
	for _, name := range []string{"get_Menu", "other_tool"} {
		v := call()
		v.Function.Name = name
		cases = append(cases, struct {
			name  string
			calls []cc.ToolCall
			want  error
		}{name, []cc.ToolCall{v}, cc.ErrProtocol})
	}
	for _, args := range []string{`{"restaurant_id":"private"}`, `{"cursor":null}`, `{"price":0}`, `{"control":true}`, `{"Open":false}`, `null`, `[]`, `{} {}`, `{"x":1,"x":2}`} {
		v := call()
		v.Function.Arguments = args
		want := menuengine.ErrProtocol
		if args == `null` || args == `[]` || args == `{} {}` || args == `{"x":1,"x":2}` {
			want = cc.ErrProtocol
		}
		cases = append(cases, struct {
			name  string
			calls []cc.ToolCall
			want  error
		}{args, []cc.ToolCall{v}, want})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			c, tr := testClient(t, func(int, wireRequest) (*http.Response, error) {
				return response(toolMessage(tc.calls...), "tool_calls"), nil
			})
			got, err := newEngine(t, c, func(context.Context) (catalog.MenuSnapshot, error) { reads++; return menuFixture(), nil }).Run(t.Context(), "menu")
			zeroError(t, got, err, tc.want)
			if reads != 0 || tr.count() != 1 {
				t.Fatal("rejected dispatch caused lookup/continuation")
			}
		})
	}
}

func TestMenuTurnReaderFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		read menuengine.MenuReader
		want error
	}{
		{"arbitrary", func(context.Context) (catalog.MenuSnapshot, error) { return menuFixture(), errors.New(privateText) }, menuengine.ErrMenuUnavailable},
		{"stored data", func(context.Context) (catalog.MenuSnapshot, error) {
			s := menuFixture()
			s.Items[0].PriceMinor = -1
			return s, nil
		}, menuengine.ErrMenuUnavailable},
		{"nil slice", func(context.Context) (catalog.MenuSnapshot, error) {
			s := menuFixture()
			s.Items = nil
			return s, nil
		}, menuengine.ErrMenuUnavailable},
		{"snapshot limit", func(context.Context) (catalog.MenuSnapshot, error) {
			return catalog.MenuSnapshot{}, fmt.Errorf("%s: %w", privateText, catalog.ErrSnapshotLimit)
		}, menuengine.ErrResource},
		{"projected limit", func(context.Context) (catalog.MenuSnapshot, error) {
			s := menuFixture()
			s.Items = make([]catalog.Item, 1001)
			return s, nil
		}, menuengine.ErrResource},
		{"wrapped cancellation", func(context.Context) (catalog.MenuSnapshot, error) {
			return menuFixture(), fmt.Errorf("%s: %w", privateText, context.Canceled)
		}, context.Canceled},
		{"wrapped deadline", func(context.Context) (catalog.MenuSnapshot, error) {
			return menuFixture(), fmt.Errorf("%s: %w", privateText, context.DeadlineExceeded)
		}, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			c, tr := testClient(t, func(int, wireRequest) (*http.Response, error) {
				return response(toolMessage(call()), "tool_calls"), nil
			})
			got, err := newEngine(t, c, func(ctx context.Context) (catalog.MenuSnapshot, error) { reads++; return tc.read(ctx) }).Run(t.Context(), "menu")
			zeroError(t, got, err, tc.want)
			if tr.count() != 1 || reads != 1 {
				t.Fatal("reader failure produced further effects")
			}
		})
	}
}

func TestMenuTurnContinuationFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		respond func() (*http.Response, error)
		want    error
	}{
		{"second tool", func() (*http.Response, error) {
			v := call()
			v.ID = "menu_call_2"
			return response(toolMessage(v), "tool_calls"), nil
		}, menuengine.ErrProtocol},
		{"provider transport", func() (*http.Response, error) { return nil, errors.New(privateText) }, cc.ErrUnavailable},
		{"provider denial", func() (*http.Response, error) {
			return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(privateText))}, nil
		}, cc.ErrAccess},
		{"reasoning only", func() (*http.Response, error) {
			return response(cc.Message{Role: cc.RoleAssistant, ReasoningContent: ptr(privateText)}, "stop"), nil
		}, cc.ErrProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			c, tr := testClient(t, func(n int, _ wireRequest) (*http.Response, error) {
				if n == 1 {
					return response(toolMessage(call()), "tool_calls"), nil
				}
				return tc.respond()
			})
			got, err := newEngine(t, c, func(context.Context) (catalog.MenuSnapshot, error) { reads++; return menuFixture(), nil }).Run(t.Context(), "menu")
			zeroError(t, got, err, tc.want)
			if tr.count() != 2 || reads != 1 {
				t.Fatal("unbounded continuation")
			}
			if tc.name == "provider denial" {
				var status *cc.HTTPError
				if !errors.As(err, &status) || status.StatusCode != 403 {
					t.Fatal("safe HTTP identity lost")
				}
			}
		})
	}
	// First-leg errors must also pass through without dispatching a reader.
	c, tr := testClient(t, func(int, wireRequest) (*http.Response, error) { return nil, errors.New(privateText) })
	reads := 0
	got, err := newEngine(t, c, func(context.Context) (catalog.MenuSnapshot, error) { reads++; return menuFixture(), nil }).Run(t.Context(), "menu")
	zeroError(t, got, err, cc.ErrUnavailable)
	if reads != 0 || tr.count() != 1 {
		t.Fatal("first-leg failure effects")
	}
}

func TestMenuTurnContextBoundaries(t *testing.T) {
	for _, earlier := range []bool{false, true} {
		t.Run(fmt.Sprint(earlier), func(t *testing.T) {
			ctx := t.Context()
			stop := func() {}
			wantDeadline := time.Time{}
			if earlier {
				ctx, stop = context.WithTimeout(ctx, 3*time.Second)
				wantDeadline, _ = ctx.Deadline()
			}
			defer stop()
			start := time.Now()
			reads := 0
			c, _ := testClient(t, func(n int, _ wireRequest) (*http.Response, error) {
				if n == 1 {
					return response(toolMessage(call()), "tool_calls"), nil
				}
				return answer(), nil
			})
			e := newEngine(t, c, func(ctx context.Context) (catalog.MenuSnapshot, error) {
				reads++
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Error("reader lacks deadline")
				}
				if earlier {
					if !deadline.Equal(wantDeadline) {
						t.Error("earlier deadline replaced")
					}
				} else if deadline.Before(start.Add(19*time.Second)) || deadline.After(time.Now().Add(20*time.Second)) {
					t.Error("reader missing 20-second cap")
				}
				return menuFixture(), nil
			})
			if _, err := e.Run(ctx, "menu"); err != nil || reads != 1 {
				t.Fatalf("deadline propagation %v", err)
			}
		})
	}
	for _, boundary := range []string{"after first model", "after reader", "after second model"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reads := 0
			c, tr := testClient(t, func(n int, _ wireRequest) (*http.Response, error) {
				if n == 1 {
					if boundary == "after first model" {
						cancel()
					}
					return response(toolMessage(call()), "tool_calls"), nil
				}
				if boundary == "after second model" {
					cancel()
				}
				return answer(), nil
			})
			e := newEngine(t, c, func(context.Context) (catalog.MenuSnapshot, error) {
				reads++
				if boundary == "after reader" {
					cancel()
				}
				return menuFixture(), nil
			})
			got, err := e.Run(ctx, "menu")
			zeroError(t, got, err, context.Canceled)
			wantReads, wantCalls := 1, 1
			if boundary == "after first model" {
				wantReads = 0
			}
			if boundary == "after second model" {
				wantCalls = 2
			}
			if reads != wantReads || tr.count() != wantCalls {
				t.Fatal("cancellation allowed further I/O")
			}
		})
	}
	t.Run("blocked reader cancellation", func(t *testing.T) {
		entered := make(chan struct{})
		done := make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		c, tr := testClient(t, func(int, wireRequest) (*http.Response, error) {
			return response(toolMessage(call()), "tool_calls"), nil
		})
		e := newEngine(t, c, func(ctx context.Context) (catalog.MenuSnapshot, error) {
			close(entered)
			<-ctx.Done()
			return catalog.MenuSnapshot{}, ctx.Err()
		})
		type outcome struct {
			result menuengine.Result
			err    error
		}
		out := make(chan outcome, 1)
		go func() {
			defer close(done)
			got, err := e.Run(ctx, "menu")
			out <- outcome{got, err}
		}()
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("reader goroutine did not exit")
			}
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("reader barrier not reached")
		}
		cancel()
		select {
		case got := <-out:
			zeroError(t, got.result, got.err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("cancellation did not return")
		}
		if tr.count() != 1 {
			t.Fatal("cancelled reader continued")
		}
	})
}

func TestMenuTurnOwnedSnapshotIntegration(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	a, err := catalog.OpenStore(t.Context(), dsn, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := catalog.OpenStore(t.Context(), dsn, "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	owned := menuFixture()
	owned.Settings.Version = 0
	for i := range owned.Items {
		owned.Items[i].Version = 0
	}
	foreign := menuFixture()
	foreign.Settings.Open = true
	foreign.Settings.Version = 0
	foreign.Items = foreign.Items[:1]
	foreign.Items[0].ID = "20000000-0000-4000-8000-000000000001"
	foreign.Items[0].Name = "other restaurant"
	foreign.Items[0].Version = 0
	if err := a.Initialize(t.Context(), owned.Settings, owned.Items); err != nil {
		t.Fatal(err)
	}
	if err := b.Initialize(t.Context(), foreign.Settings, foreign.Items); err != nil {
		t.Fatal(err)
	}
	beforeA, err := a.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	beforeB, err := b.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pageA, err := a.Page(t.Context(), 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	pageB, err := b.Page(t.Context(), 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, tr := testClient(t, func(n int, w wireRequest) (*http.Response, error) {
		if n == 1 {
			return response(toolMessage(call()), "tool_calls"), nil
		}
		if w.Messages[3].Content == nil || *w.Messages[3].Content != expectedWire(t, beforeA) {
			t.Fatal("bound restaurant/closed/unavailable snapshot lost")
		}
		return answer(), nil
	})
	got, err := newEngine(t, c, a.Snapshot).Run(t.Context(), "menu")
	if err != nil || got.Menu == nil || got.Menu.Open || tr.count() != 2 {
		t.Fatalf("database-backed turn %+v %v", got, err)
	}
	for i, s := range []*catalog.Store{a, b} {
		after, err := s.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		page, err := s.Page(t.Context(), 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, []catalog.MenuSnapshot{beforeA, beforeB}[i]) || !reflect.DeepEqual(page, []catalog.Page{pageA, pageB}[i]) {
			t.Fatal("turn changed catalog records/settings/versions/Page")
		}
	}
}
