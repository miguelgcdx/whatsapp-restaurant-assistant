# Cart-edit proposal decoder

`internal/cartintent.Decode(arguments string, trustedCart cart.CartID) (cart.Call, error)`
turns untrusted model JSON into **one proposal**, built with the public `cart`
constructors. The caller supplies the trusted cart separately; JSON cannot choose
it. This package does not register, authorize, execute, persist, or invoke a model.
There is no runtime or consumer integration in this increment.

## Accepted protocol

Exactly these three action shapes are supported (field order is irrelevant):

```json
{"expected_version":"0","action":"add","menu_id":"10000000-0000-4000-8000-000000000001","count":2}
{"expected_version":"0","action":"remove","unit_ids":["10000000-0000-4000-8000-000000000002"]}
{"expected_version":"0","action":"set_note","unit_ids":["10000000-0000-4000-8000-000000000002"],"note":"one without onions"}
```

- Every field shown for the selected action is required; no other fields are
  accepted. No batch, create, read, fulfillment, summary, submission, implicit
  target selection, or authority/price override is supported.
- `expected_version` is a canonical nonnegative int64 decimal **string**, parsed
  by `cart.Version.UnmarshalText`. Signs, leading zeros (except `"0"`), whitespace,
  fractions, exponents, and overflow are rejected.
- `count` is a positive int64 JSON integer token. Strings, fractions, exponents,
  overflow, zero, and negative counts are rejected; no float conversion occurs.
- IDs use `cart.ParseMenuID` and `cart.ParseUnitID`, not a UUID-v4-only rule.
  Targets must be nonempty, unique, and accepted by the domain constructors.
- Notes preserve exact Unicode and whitespace: at most 500 runes, no NUL.
  An empty note clears the targeted notes. Nothing is trimmed or repaired.

## Strictness and limits

The inclusive input budget is 512 × 1024 **bytes**, checked before decoding.
This is a local engineering budget, not a quantity or execution guarantee.
Invalid raw UTF-8 and lone Unicode-surrogate escapes are rejected rather than
silently replaced. Literal U+FFFD, `\uFFFD`, and valid surrogate pairs are accepted.
Field names are case-sensitive after JSON escape decoding. Duplicate decoded
keys (including escape-equivalent keys), unknown/missing/extra fields, nulls,
wrong types, non-object roots, malformed JSON, and trailing data are rejected.

Every rejection returns a zero `cart.Call` and the exact static
`cartintent.ErrInput`, without wrapping, logging, or including raw arguments.
Every success returns the full `cart.EditCall` result unchanged, using `cart.Add`,
`cart.Remove`, or `cart.SetNote`. A zero trusted cart is rejected by the domain.

IDs prove syntax only—not existence, ownership, availability, or capacity.
Decoding proves neither action permission, semantic model quality, native
consumer integration, nor an end-to-end customer outcome. Those remain separate
validation and execution responsibilities.

## Offline verification

Public-boundary tests compare whole Calls with independently constructor-built
Calls, and check exact error identity and zero results on rejection. The focused
package suite, race check, and vet require no database, server, or live provider.
