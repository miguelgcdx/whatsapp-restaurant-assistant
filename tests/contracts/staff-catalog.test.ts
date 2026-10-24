import { expect, it } from "vitest";
import {
  catalogQuery,
  catalogSchema,
  formatPrice,
} from "../../src/contracts/staff-catalog.ts";

const price = {
  amount_minor: "9223372036854775807",
  currency: "KWD",
  exponent: 3,
};
const item = {
  id: "123e4567-e89b-42d3-a456-426614174000",
  name: "🍲 Café",
  price,
  available: false,
  version: "0",
};
const page = {
  data: {
    items: [item],
    next_cursor: null,
    restaurant: { open: false, version: "0" },
  },
  request_id: "a".repeat(32),
};
it("validates exact catalog envelopes, Unicode names and canonical int64 scalars", () => {
  expect(catalogSchema.parse(page)).toEqual(page);
  expect(
    catalogSchema.parse({ ...page, data: { ...page.data, items: [] } }).data
      .items,
  ).toEqual([]);
  for (const change of [
    { version: "01" },
    { version: "not-decimal" },
    { version: "-1" },
    { version: "1.0" },
    { version: 1 },
    { version: "9223372036854775808" },
    { id: item.id.toUpperCase() },
    { name: "\u2003" },
    { name: "\u0085" },
    { name: "a\0b" },
    { name: "x".repeat(201) },
    { name: "\ud800" },
    { price: { ...price, currency: "usd" } },
    { price: { ...price, exponent: 7 } },
    { price: { ...price, amount_minor: 42 } },
    { token: "secret" },
  ]) {
    expect(
      catalogSchema.safeParse({
        ...page,
        data: { ...page.data, items: [{ ...item, ...change }] },
      }).success,
    ).toBe(false);
  }
  expect(
    catalogSchema.parse({
      ...page,
      data: { ...page.data, items: [{ ...item, name: "🍲".repeat(200) }] },
    }).data.items[0]?.name,
  ).toBe("🍲".repeat(200));
  expect(catalogSchema.safeParse({ ...page, secret: "no" }).success).toBe(
    false,
  );
  expect(
    catalogSchema.safeParse({
      ...page,
      data: { ...page.data, next_cursor: " " },
    }).success,
  ).toBe(false);
});
it("formats configured exponents without losing MaxInt64 precision", () => {
  expect(formatPrice(price)).toBe("KWD 9223372036854775.807");
  expect(
    formatPrice({ ...price, amount_minor: "5", currency: "JPY", exponent: 0 }),
  ).toBe("JPY 5");
  expect(formatPrice({ ...price, amount_minor: "5", exponent: 6 })).toBe(
    "KWD 0.000005",
  );
});
it("validates the only query fields without duplicate, encoding or authority ambiguity", () => {
  expect(catalogQuery("").toString()).toBe("");
  expect(catalogQuery("?limit=1&cursor=abc%2Bdef").toString()).toBe(
    "limit=1&cursor=abc%2Bdef",
  );
  expect(catalogQuery("?limit=100").get("limit")).toBe("100");
  for (const query of [
    "?",
    "?limit=0",
    "?limit=101",
    "?limit=01",
    "?limit=1&limit=2",
    "?cursor=",
    "?cursor=a+b",
    "?cursor=%FF",
    "?cursor=%",
    "?restaurant_id=x",
    `?cursor=${"x".repeat(2049)}`,
  ])
    expect(() => catalogQuery(query)).toThrow();
});
