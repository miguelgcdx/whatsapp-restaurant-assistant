import { describe, expect, it, vi } from "vitest";
import {
  databaseTarget,
  localOrigin,
  originAllowed,
} from "../../src/server/auth/config.ts";

// Only the bundler server-only marker is stubbed; authentication is never mocked.
vi.mock("server-only", () => ({}));

describe("explicit local targets", () => {
  it("parses an explicit target, never driver-overridable query configuration", () => {
    expect(
      databaseTarget(
        "postgres://fixture:password@127.0.0.1:5544/restaurant_foundation_test?sslmode=disable",
      ),
    ).toEqual({
      host: "127.0.0.1",
      port: 5544,
      database: "restaurant_foundation_test",
      user: "fixture",
      password: "password",
      ssl: false,
    });
    for (const value of [
      "postgres://fixture@localhost:5544/restaurant_foundation_test",
      "postgres://fixture@127.0.0.1/restaurant_foundation_test",
      "postgres://fixture@127.0.0.1:0/restaurant_foundation_test",
      "postgres://fixture@127.0.0.1:5544/postgres",
      "postgres://fixture@127.0.0.1:5544/restaurant_foundation_test?host=remote",
      "postgres://fixture@127.0.0.1:5544/restaurant_foundation_test?options=-csearch_path=public",
      "postgres://127.0.0.1:5544/restaurant_foundation_test",
    ])
      expect(() => databaseTarget(value)).toThrow(
        "Local authentication configuration is unavailable.",
      );
  });
  it("requires canonical numeric loopback origins without URL extras", () => {
    expect(localOrigin("http://127.0.0.1:3000")).toBe("http://127.0.0.1:3000");
    for (const value of [
      "http://localhost:3000",
      "http://127.0.0.1:3000/",
      "http://127.0.0.1:3000/path",
      "http://user@127.0.0.1:3000",
      "https://example.test",
    ])
      expect(() => localOrigin(value)).toThrow();
  });
  it("rejects missing/foreign write origins without fetch-site or forwarded-host fallback", () => {
    const origin = "http://127.0.0.1:3000";
    expect(originAllowed(new Headers(), origin, false)).toBe(true);
    expect(originAllowed(new Headers({ origin }), origin, true)).toBe(true);
    expect(
      originAllowed(
        new Headers({
          "sec-fetch-site": "same-origin",
          "x-forwarded-host": "127.0.0.1:3000",
        }),
        origin,
        true,
      ),
    ).toBe(false);
    expect(
      originAllowed(
        new Headers({ origin: "http://127.0.0.1:3001" }),
        origin,
        false,
      ),
    ).toBe(false);
  });
});
