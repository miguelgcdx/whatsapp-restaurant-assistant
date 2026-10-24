import { expect, it } from "vitest";
import {
  beginIntent,
  intentOutcome,
  retryIntent,
  settleIntent,
} from "../../src/app/dashboard/catalog/availability-intent.ts";
import {
  availabilityBody,
  itemAvailabilitySchema,
  restaurantAvailabilitySchema,
} from "../../src/contracts/staff-availability.ts";

it("accepts flat JSON semantics but rejects duplicate/extra/null/numeric/noncanonical versions", () => {
  expect(
    availabilityBody(
      ' {"available":false, "expected_version":"9223372036854775807"} ',
      "item",
    ),
  ).toEqual({ available: false, expected_version: "9223372036854775807" });
  expect(
    availabilityBody('{"expected_version":"0","availa\\u0062le":true}', "item"),
  ).toEqual({ available: true, expected_version: "0" });
  for (const text of [
    '{"expected_version":"0","available":true,"available":false}',
    '{"expected_version":"0","available":true,"availa\\u0062le":false}',
    '{"expected_version":0,"available":true}',
    '{"expected_version":"01","available":true}',
    '{"expected_version":"9223372036854775808","available":true}',
    '{"expected_version":"-1","available":true}',
    '{"expected_version":null,"available":true}',
    '{"expected_version":"0","available":null}',
    '{"expected_version":"0","available":true,"role":"admin"}',
    '{"expected_version":"0","available":true} {}',
    '{"expected_version":"0"}',
    "[]",
  ])
    expect(() => availabilityBody(text, "item")).toThrow();
  expect(
    availabilityBody('{"expected_version":"0","open":true}', "restaurant"),
  ).toEqual({ expected_version: "0", open: true });
});
it("derives strict mutation DTOs from the catalog without numeric money conversion", () => {
  const item = {
    id: "123e4567-e89b-42d3-a456-426614174000",
    name: "<b>food</b>",
    price: {
      amount_minor: "9223372036854775807",
      currency: "KWD",
      exponent: 3,
    },
    available: false,
    version: "1",
  };
  expect(
    itemAvailabilitySchema.parse({ data: item, request_id: "a".repeat(32) })
      .data.price.amount_minor,
  ).toBe("9223372036854775807");
  expect(
    itemAvailabilitySchema.safeParse({
      data: { ...item, token: "secret" },
      request_id: "a".repeat(32),
    }).success,
  ).toBe(false);
  expect(
    restaurantAvailabilitySchema.safeParse({
      data: { open: true, version: 1 },
      request_id: "a".repeat(32),
    }).success,
  ).toBe(false);
});
it("freezes uncertain intent identity through refresh and allows only explicit retry, then a deliberate new intent", () => {
  const first = beginIntent("item", "4", false, "one");
  const uncertain = settleIntent(first, "uncertain");
  expect(retryIntent(uncertain)).toEqual(first);
  expect(retryIntent(uncertain).intent).toBe(first.intent);
  expect(first.intent.body).toBe('{"expected_version":"4","available":false}');
  expect(first.intent.key).toBe("one");
  const conflict = settleIntent(first, "conflict");
  expect(() => retryIntent(conflict)).toThrow();
  expect(beginIntent("item", "6", true, "two").intent).toEqual({
    key: "two",
    body: '{"expected_version":"6","available":true}',
  });
  expect(settleIntent(first, "success").kind).toBe("success");
});
it("classifies only validated matching acknowledgments and definite errors; unknown commit outcomes retain intent", () => {
  const pending = beginIntent("restaurant", "4", false, "stable-key");
  const target = { kind: "restaurant" } as const;
  const success = {
    data: { open: false, version: "5" },
    request_id: "a".repeat(32),
  };
  expect(intentOutcome(200, success, target, pending)).toBe("success");
  for (const value of [
    null,
    { ...success, data: { open: true, version: "5" } },
    { ...success, data: { open: false, version: "6" } },
    { ...success, token: "secret" },
  ])
    expect(intentOutcome(200, value, target, pending)).toBe("uncertain");
  for (const [status, code, expected] of [
    [409, "version_conflict", "conflict"],
    [409, "idempotency_conflict", "conflict"],
    [401, "unauthenticated", "denied"],
    [403, "forbidden", "denied"],
    [404, "not_found", "rejected"],
    [400, "invalid_input", "rejected"],
    [413, "payload_too_large", "rejected"],
    [415, "unsupported_media_type", "rejected"],
    [500, "internal_error", "uncertain"],
    [503, "temporarily_unavailable", "uncertain"],
    [409, "forbidden", "uncertain"],
    [302, "temporarily_unavailable", "uncertain"],
    [201, "temporarily_unavailable", "uncertain"],
  ] as const) {
    const outcome = intentOutcome(
      status,
      { error: { code, message: "opaque" }, request_id: "a".repeat(32) },
      target,
      pending,
    );
    expect(outcome).toBe(expected);
    if (outcome === "uncertain")
      expect(retryIntent(settleIntent(pending, outcome)).intent).toBe(
        pending.intent,
      );
  }
  const id = "123e4567-e89b-42d3-a456-426614174000";
  const itemPending = beginIntent("item", "0", true, "item-key");
  const item = {
    id,
    name: "Food",
    price: { amount_minor: "1", currency: "USD", exponent: 2 },
    version: "1",
    available: true,
  };
  expect(
    intentOutcome(
      200,
      { data: item, request_id: "a".repeat(32) },
      { kind: "item", id },
      itemPending,
    ),
  ).toBe("success");
  expect(
    intentOutcome(
      200,
      {
        data: { ...item, id: "223e4567-e89b-42d3-a456-426614174000" },
        request_id: "a".repeat(32),
      },
      { kind: "item", id },
      itemPending,
    ),
  ).toBe("uncertain");
});
