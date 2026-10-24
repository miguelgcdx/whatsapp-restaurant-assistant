import { expect, it } from "vitest";
import {
  catalogQuery,
  catalogSchema,
  formatPrice,
} from "../../src/contracts/staff-catalog.ts";

const item = {
  id: "00000000-0000-4000-8000-000000000001",
  name: "Soup",
  price: { amount_minor: "0", currency: "BHD", exponent: 3 },
  available: true,
  version: "9223372036854775807",
};
const page = {
  data: {
    items: [item],
    next_cursor: null,
    restaurant: { open: false, version: "0" },
  },
  request_id: "0".repeat(32),
};

it("bounds continuations and rejects impossible empty continuation pages", () => {
  for (const length of [2048, 2049]) {
    const next_cursor = "x".repeat(length);
    expect(
      catalogSchema.safeParse({
        ...page,
        data: { ...page.data, next_cursor },
      }).success,
    ).toBe(length === 2048);
  }
  expect(
    catalogSchema.safeParse({
      ...page,
      data: { ...page.data, items: [], next_cursor: "x" },
    }).success,
  ).toBe(false);
  expect(
    catalogSchema.safeParse({
      ...page,
      data: { ...page.data, items: Array(101).fill(item) },
    }).success,
  ).toBe(false);
});

it("rejects currency or exponent disagreement within one catalog page", () => {
  for (const price of [
    { ...item.price, currency: "JPY" },
    { ...item.price, exponent: 0 },
  ]) {
    expect(
      catalogSchema.safeParse({
        ...page,
        data: { ...page.data, items: [item, { ...item, price }] },
      }).success,
    ).toBe(false);
  }
});

it("preserves zero and MaxInt64 exactly at every supported exponent", () => {
  const amount_minor = "9223372036854775807";
  for (let exponent = 0; exponent <= 6; exponent++) {
    const expected =
      exponent === 0
        ? amount_minor
        : `${amount_minor.slice(0, -exponent)}.${amount_minor.slice(-exponent)}`;
    expect(formatPrice({ ...item.price, amount_minor, exponent })).toBe(
      `BHD ${expected}`,
    );
    expect(formatPrice({ ...item.price, exponent })).toBe(
      `BHD ${exponent === 0 ? "0" : `0.${"0".repeat(exponent)}`}`,
    );
  }
});

it("rejects encoded duplicates, malformed UTF-8 and URL selectors", () => {
  for (const query of [
    "?limit=1&%6cimit=2",
    "?cursor=x&cursor=y",
    "?limit=1&",
    "?cursor=%00",
    "?cursor=%C0%AF",
    "?cursor=%ED%A0%80",
    "?url=http%3A%2F%2F127.0.0.1%3A1",
    "?limit=1.0",
    "?limit=-1",
    `?cursor=${"x".repeat(2049)}`,
  ]) {
    expect(() => catalogQuery(query)).toThrow();
  }
});
