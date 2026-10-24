import { describe, expect, it } from "vitest";
import { staffAccessSchema } from "../../src/contracts/staff-access.ts";

describe("the minimal staff access wire contract", () => {
  it("accepts only a restaurant identity, never caller authority or token fields", () => {
    const valid = { restaurant_id: "4d307bb0-c7e3-4d91-b14c-c990fd967a38" };
    expect(staffAccessSchema.parse(valid)).toEqual(valid);
    for (const value of [
      null,
      {},
      { restaurant_id: "not-a-uuid" },
      { restaurant_id: valid.restaurant_id, role: "admin" },
      { restaurant_id: valid.restaurant_id, token: "must-not-reach-a-browser" },
    ]) {
      expect(staffAccessSchema.safeParse(value).success).toBe(false);
    }
  });
});
