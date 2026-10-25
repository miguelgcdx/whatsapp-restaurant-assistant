# Run a bounded, read-only menu proposal turn

`menuengine` combines the explicit CommandCode client with a server-bound catalog
reader. It can propose a direct reply or perform one `get_menu` round and return
an authoritative stored open-status field alongside the final model proposal.
It is an isolated library: no worker, HTTP/CLI entry point, or automatic customer
reply is wired here.

## Caller contract

```go
type MenuReader func(context.Context) (catalog.MenuSnapshot, error)
type Config struct {
    Client          *commandcode.Client
    ReadMenu        MenuReader
    MaxOutputTokens int
}
func New(Config) (*Engine, error)
func (*Engine) Run(context.Context, string) (Result, error)

type Result struct {
    Reply string
    Menu  *MenuStatus
}
type MenuStatus struct { Open bool }
```

Construct the concrete client explicitly, then bind `ReadMenu` to a trusted
`catalog.Store.Snapshot` method. The model cannot choose a restaurant, client,
credential, sender, event, cursor, control flag, or price. Configuration requires
a nonnil client/function and an explicit output-token budget from 1 through 8192.
No ambient keys, default client, or configuration lookup is performed.

**Before calling Run, a future application must establish current conversation,
control, and read authority, including before model invocation.** This component
is not that gate. It does not implement missing persistent staff takeover state
or offer a permission flag that pretends to do so. The reader and transport are
trusted dependencies; concurrent callers must supply concurrency-safe dependencies.

## Two possible successful paths

| Path | Model calls / reads | Result |
| --- | --- | --- |
| Direct first answer | 1 / 0 | `Reply` contains content only; `Menu=nil` makes no catalog-grounding claim. |
| Menu tool round | 2 / 1 | `Reply` is final content only; nonnil `Menu.Open` is the exact stored status. |

Every run starts fresh system/user history and preserves nonblank user text
without normalization. Nil context, invalid receiver/configuration, blank text,
invalid UTF-8, or NUL is rejected before I/O. No private reasoning is returned as
a reply or fallback, and no history is logged, persisted, or shared across runs.

The only declared tool is `get_menu`, with an object schema containing empty
properties and `additionalProperties:false`. Application dispatch requires
**exactly one call and an exactly empty JSON object**. Unknown names, selectors,
nonempty arguments, null, arrays, trailing data, malformed/duplicate calls, and
multiple calls cause no menu read. Schema declaration is not a claim that a
provider enforces strict structured output.

The continuation replays the original assistant message unchanged, including
nullable/empty content, nil/empty/nonempty reasoning, call identity, and the raw
argument string. One matching tool-result message follows it; the tool declaration
and output-token budget stay the same. A second tool request is rejected, never
dispatched: there is no third call or second read.

## Complete tool data, including closed menus

The validated snapshot is encoded as a complete JSON object:

```json
{"open":false,"items":[{"id":"10000000-0000-4000-8000-000000000001","name":"Soup","available":false,"price":{"amount_minor":"650","currency":"USD","exponent":2}}]}
```

Empty menus encode `items:[]`. Unavailable items remain present; prices are exact
decimal integer strings, not floating-point amounts. No fees, estimates, order
state, or calculated totals are added. Reading and encoding do not mutate the
snapshot or database. See [the snapshot contract](menu-snapshot.md) for atomicity,
restaurant scope, and its separate local execution budgets.

A closed restaurant is browsable with explicit `open:false` and
`Result.Menu.Open=false`. Browsing does not reopen it or authorize an order.
All reply text is a **model proposal**, not confirmation, permission, or a
semantic-quality guarantee. Stored menu status is authoritative structured data;
a fabricated transport test cannot establish live language quality.

## Bounds and safe failures

- At most two model attempts and one menu read; no automatic retry or partial
  dispatch. Every error returns `Result{}`, including after partial first content
  or a successful menu read. There is no customer-facing fallback wording.
- Overall cooperative context cap: **20 seconds**, honoring earlier caller
  deadlines. Context is checked at effect boundaries. The concrete client and
  catalog reader retain their own tighter operation limits. No detached watchdog
  goroutine can contain a trusted callback that ignores cancellation.
- Complete serialized tool JSON: **256 KiB inclusive**. Larger output or
  `catalog.ErrSnapshotLimit` becomes `ErrResource`, with no continuation or
  truncated menu. These are local safety budgets, not restaurant/provider policy.
- Invalid snapshots and arbitrary reader errors become `ErrMenuUnavailable`.
  Known cancellation/deadline identities are preserved without sensitive wrappers.
  Already-safe concrete client errors, including typed numeric HTTP errors, pass
  through. Raw reader/provider strings, SQL/DSN data, credentials, prompts, and
  reasoning are not added to error messages.

The engine's static exported errors are `ErrInput` ("invalid menu turn input"),
`ErrProtocol` ("invalid menu turn response"), `ErrMenuUnavailable` ("menu temporarily
unavailable"), and `ErrResource` ("menu turn exceeds local budget"). The concrete
client can reject malformed provider responses before engine dispatch and return
its own safe protocol identity.

## Offline proof

From `backend`, using the approved clean offline Go environment and isolated
PostgreSQL prerequisites:

```sh
go test -count=1 -timeout=20s ./internal/menuengine
go test -race -count=1 -timeout=20s ./internal/menuengine
go vet ./internal/menuengine
```

All model traffic uses fabricated keys and injected in-memory RoundTrippers,
never the default network transport. Tests inspect the actual URL, model,
ordered roles, schema, budgets, complete JSON, and replay states; independently
encoded fixtures exercise exactly 262144 and 262145 bytes. Barrier-based
cancellation tests avoid sleeps, and callbacks inspect the propagated deadlines
without waiting 20 seconds. Repeated/concurrent runs check history isolation.
One disposable PostgreSQL integration binds the real snapshot reader, verifies
closed/unavailable two-restaurant scope, and compares public Snapshot/Page values
before and after. Missing PostgreSQL prerequisites fail rather than skip.

This proof does not cover a deployed entry point, persisted authorization state,
remote cancellation, live model behavior, or order execution. Feature-end combined
verification and application integration remain separate responsibilities.
