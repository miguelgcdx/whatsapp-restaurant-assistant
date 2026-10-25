# Offline-tested CommandCode one-turn boundary

`internal/commandcode` is an isolated standard-library Go HTTP/codec client for
`deepseek/deepseek-v4.1-flash`. It performs **one non-streaming completion per
explicit `Complete` call**. It is not wired into the worker, restaurant tools,
customer conversations, authorization, confirmation, summary or ordering flows.
Tool calls are untrusted data, not permission to execute an operation.

## Quick path: fabricated transport only

This native example intercepts every request. It reads no environment key, opens
no listener and makes no network call. The injected transport is trusted code;
never let a customer or model supply one.

```go
package example

import (
    "context"
    "io"
    "net/http"
    "strings"

    "restaurant-assistant/backend/internal/commandcode"
)

type fixtureTransport struct{}

func (fixtureTransport) RoundTrip(*http.Request) (*http.Response, error) {
    return &http.Response{
        StatusCode: http.StatusOK,
        Header: http.Header{"Content-Type": []string{"application/json"}},
        Body: io.NopCloser(strings.NewReader(`{"choices":[{"index":0,"message":{"role":"assistant","content":"Synthetic answer."},"finish_reason":"stop"}]}`)),
    }, nil
}

func syntheticTurn(ctx context.Context) (commandcode.Result, error) {
    client, err := commandcode.New(commandcode.Config{
        APIKey: "fabricated-example-key", Transport: fixtureTransport{},
    })
    if err != nil {
        return commandcode.Result{}, err
    }
    text := "Synthetic fixture question."
    return client.Complete(ctx, commandcode.Request{
        Messages: []commandcode.Message{{Role: commandcode.RoleUser, Content: &text}},
        MaxOutputTokens: 64,
    })
}
```

`New` requires an explicit trimmed key; there is no environment or credential-file
fallback. Nil `Transport` selects the private network-capable default. Do not use
that default to run this fixture or infer approval for live calls.

## Request and replay contract

| Topic | Contract |
|---|---|
| Fixed route | POST `https://api.commandcode.ai/provider/v1/chat/completions`, Bearer auth, JSON Content-Type and Accept |
| Fixed model | `deepseek/deepseek-v4.1-flash`; no base URL, model or arbitrary-header override |
| Generation | Explicit `MaxOutputTokens` 1–8192, `stream:false`; tools use standard function declarations and `tool_choice:"auto"` |
| Tools | Server-owned names (1–64 ASCII letters/digits/underscore/hyphen), descriptions and object `Parameters`; unique declarations |
| Calls | Unique IDs (1–128 of the same syntactic characters), `Type:"function"`, declared function names and valid object JSON `Arguments` strings |
| History | Ordered system/user/assistant/tool messages; a tool result references an earlier assistant call exactly once |
| Text | Valid UTF-8 without NUL; role-specific fields reject identity/role confusion |
| Ownership | Inputs remain unchanged; caller must not concurrently mutate their referenced data |

Use `Content: nil` for null assistant content and `Content: &empty` for an explicit
empty string. `ReasoningContent: nil` represents absent or null reasoning and is
omitted on replay; `&empty` remains `""`. Nonempty reasoning remains byte-for-byte
string data. There is no backfill, reasoning normalization or thinking-disable
option. Earlier assistant reasoning stays in history even before later user turns.
Reasoning is sensitive opaque history: never log it or display it as an answer.

For a tool turn, inspect the validated `Result.Message.ToolCalls` as data. Any future
orchestrator must separately authorize scoped internal Go operations using trusted
conversation/sender/event/control context, not model-provided authority. If a future
caller chooses another turn, it must append the returned assistant message and its
own tool-result messages, then explicitly call `Complete` again. There is no tool
registry, function invocation, automatic tool loop, persistence or domain mutation.

Arguments are validated without re-encoding the inner JSON string: spaces and key
order are preserved. Tool parameter JSON is structurally checked as an object with
`type:"object"`; if present, `properties` must map to schema objects and `required`
must be an array of unique nonempty strings. Duplicate JSON keys are rejected.
This is **not a full JSON Schema validator**: it does not validate arguments against
all declared schema constraints or imply strict outputs, JSON mode or a supported
`response_format`. Parameter serialization may compact insignificant JSON whitespace.

## Budgets, responses and errors

These are local application budgets, not claims about provider limits:

- At most 128 messages, 32 tools, 1 MiB encoded request and 512 KiB response.
  Raw input is also bounded before JSON parsing/marshaling; oversized input is
  rejected without an HTTP attempt. Reads stop at 512 KiB plus one sentinel byte.
- Overall timeout is 10 seconds, with any earlier caller deadline retained.
  The private transport has 5-second dial, TLS and response-header timeouts and a
  64 KiB header budget. Environment proxies and redirects are disabled; no global
  client/transport is changed. Connections are not retained idle.
- Exactly one choice with explicit index 0 and an assistant message is required.
  Unknown extra provider metadata is ignored, not trusted; reported model aliases
  are not used as authority. Non-JSON/SSE, invalid UTF-8/JSON, duplicate keys and
  malformed modeled fields are rejected. Critical field names must use their
  canonical wire spelling; case-folded aliases cannot overwrite them.
- `stop` requires nonempty content and no calls; `tool_calls` requires one or more
  valid declared calls with unique IDs, including uniqueness against prior history.
  A reasoning-only response is never promoted to final content.
- `length` returns `ErrIncomplete`; `content_filter`, `refusal` finish or a non-null
  message refusal returns `ErrRefusal`. Unexpected finish/mismatches return
  `ErrProtocol`. Every error returns a zero `Result`, never partial content/calls.

`ErrInput`, `ErrProtocol`, `ErrResource`, `ErrAccess`, `ErrRateLimit`,
`ErrIncomplete`, `ErrRefusal`, `ErrUnavailable` and `ErrHTTP` support `errors.Is`.
`*HTTPError` exposes only numeric `StatusCode`: 401/403 map to access, 429 to rate,
5xx to unavailable, and other non-2xx (including redirects) to HTTP failure.
Provider bodies, URLs, keys, prompts and arbitrary transport/read error strings
never enter returned errors; the package emits no logs. Known cancellation/deadline
identities remain observable through `errors.Is`, without their private wrappers.
Response bodies are closed, including a body returned alongside a transport error.

There is no automatic retry on any status, transport/read error, cancellation or
ambiguous response. A cancellation or timeout does **not** establish that remote
compute stopped, billing was reversed or an external effect did not happen.
An injected transport must honor context and bound its own I/O; the client does
not start a goroutine to hide a transport that ignores cancellation. The transport
can see keys and prompts and is therefore an application trust boundary.

## Offline verification and evidence limits

From `backend`, using the already prepared offline toolchain/cache:

```sh
export GOTOOLCHAIN=local GOCACHE="$PWD/.cache/build"
export GOPATH="$PWD/.cache/gopath" GOMODCACHE="$PWD/.cache/modules"
export GOFLAGS=-mod=readonly GOPROXY=off GOSUMDB=off GOTELEMETRY=off GOWORK=off
go test -count=1 -timeout=20s ./internal/commandcode
go test -race -count=1 -timeout=20s ./internal/commandcode
go vet ./internal/commandcode
```

Tests use fabricated credentials and injected RoundTrippers only, never a real
gateway, localhost server, environment proxy, secret or database. Assertions cover
the actual HTTP URL/headers/model/body, two explicit turns with reasoning replay,
ordered histories, arguments/results, unchanged inputs, boundaries, closed bounded
bodies, redacted failures, no retries and barrier-controlled cancellation/deadlines.
The preimplementation suite produced compile RED because no production files
existed; that is not runtime behavioral RED. Supplementary cancellation-identity
checks produced runtime RED and passed after a narrow error-mapping fix. Further
self-review reproduced case-folded critical-key and null assistant-identity
acceptance, then pinned their rejection with passing public-interface tests. Commands
above are verification instructions, not a claim about a particular run; final
execution evidence and independent assessment remain parent-owned.

Primary public sources checked by the parent:

- [CommandCode provider route](https://commandcode.ai/docs/provider): endpoint,
  Bearer authorization and plan/error distinctions.
- [Selected model listing](https://commandcode.ai/models/deepseek-v4-1-flash): exact
  gateway slug, rather than the historical shorthand “deepseek 4.1 flash”.
- [Native DeepSeek thinking guidance](https://api-docs.deepseek.com/guides/thinking_mode/):
  replay all previous assistant `reasoning_content` with tools, including earlier
  non-tool turns. This is native guidance, **not proven gateway passthrough**.

No authenticated catalog or completion has been performed here. Offline tests do
not establish live account access, availability, gateway normalization, default
thinking, strict tools/structured outputs, natural-language confirmation quality,
pricing, quota or retention guarantees. Live/private testing needs separate
explicit consent and a data-sharing review: both gateway and downstream provider
receive prompts and tool data. This guide requests no real credentials or setup.

## Observed offline verification

The independent assessment initially found observable cancellation masked by
early response classification. The three-line correction and adopted public
regression reproduced RED before the fix; the limited independent recheck closed
that finding and its direct regressions. Other original assessment conclusions
retain their original scope and attribution; no new full security sweep occurred.

The final full-backend race run passed with `-count=1 -timeout=180s`, process
umask `077`, a short owned temporary directory and offline dependency settings.
This package passed in 1.530 seconds; all 104 selected maintained Go/SQL/module
inputs matched before/after the run. Earlier full normal/vet passes and refreshed
worker normal/race/vet results are distinct evidence, not a newly repeated full
normal suite. Failed runs and corrections remain in
`../../odd/tasks/local-commandcode-adapter.md` and the linked fixture trackers.
No live model request, account access, gateway reasoning passthrough, model
quality, retention, confirmation, WhatsApp delivery or production readiness is
established by these results.
