# Portable pickup completion proposal

`internal/pickupintent` implements the AI-owned, read-only FR-13 assessment port.
It is not connected order submission, payment, pricing, persistence, or staff UI.

## Trusted configuration

Construct with `New(Config{Client: client, MaxOutputTokens: tokens})`, using an
explicit existing `commandcode.Client` and a token budget from 1 through 8192.
The host fixes **pickup-completion-v1** in trusted adapter configuration, not in a
model-returned field. There are no ambient credentials, default clients, retries
or provider fallbacks. `Assess(ctx, ScopedHistoryInput)` uses a cooperative overall
20-second deadline, respecting earlier caller deadlines. The concrete client
retains its shorter per-call limit and response budget. Cancellation cannot prove
remote work stopped.

## Exact scoped context

`ScopedHistoryInput` JSON has `summary_text`, `closing_question`, `inbound_text`,
and `history`. Each history message has `source_id`, `speaker`, and `text`.
Speakers are exactly `customer` or `restaurant`: labels inside quoted DATA,
never promoted into provider roles. History contains all scoped customer-facing
intervening turns strictly AFTER acknowledged Q0 and BEFORE current stored M,
in server-stored causal order. Exact summary, closing question and current
inbound are separate fields; Q0 and M are not repeated inside history.

A nonnil empty `[]` is valid only when the host established a complete empty
window. Nil/missing/unavailable history is an error, never silently changed to
`[]` or `not_complete`. Validation requires nonblank summary/question/inbound and
history text, nonempty unique opaque source IDs, at most 62 history messages,
valid UTF-8 before marshaling, and `json.Marshal(ScopedHistoryInput)` at most
65536 bytes inclusive. No ID syntax implies authority. Content is never trimmed,
rewritten, truncated or selected last-N. Silence/blank inbound is rejected.
The host establishes authentic presentation, completeness, scope, current
cart/control state and digest binding. The DTO cannot prove those facts; no
concatenation heuristic, ordinal, current-inbound wrapper or verified flag is used.

## Tool-only result

One fixed system instruction and one serialized-input user message are sent in
one call, declaring only `assess_pickup_completion`. Exactly one returned call to
that tool must have solely `{"decision":"complete"}` or
`{"decision":"not_complete"}` as arguments. Missing/unknown/extra calls, malformed,
duplicate (including escaped duplicate), null, authority/confidence, wrong-type,
invalid-Unicode or trailing arguments fail with zero Decision and a safe error.
Content JSON and private reasoning never supply fallback decisions. Errors keep
client/context identities; failures never fabricate `not_complete`. Nothing is
logged. Tool-call IDs are not returned as authority or selectors.

FR-13 completion after anything-else needs no extra confirmation. Non-edit
clarification does not reset the summary. Wanting more, edits such as “no onions”,
unresolved edits, unrelated assent and “maybe” are not completion. The host must
validate any proposal before submission, preserving its digest/source/version.

## Evidence and limits

Public-API offline tests use fabricated credentials and in-memory transport to
check wire/tool schema, exact context, bounds, cancellation, immutability and
zero/one attempts. Scripted P/N/R/S examples prove protocol plumbing, not language
understanding or injection resistance. Connected authority, stale-state rejection,
atomic order/replay and staff views remain backend-owned, held and unverified.
Risk item 2 is this untrusted confirmation-proposal boundary: self-checks precede
a separate parent-owned independent verification. Earlier backend0126ed21 checks
are historical, superseded evidence, not acceptance of this contract.
