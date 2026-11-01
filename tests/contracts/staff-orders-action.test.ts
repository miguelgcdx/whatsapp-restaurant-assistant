import { Children, isValidElement, type ReactNode } from "react";
import { expect, it } from "vitest";
import { ActionOptions } from "../../src/app/dashboard/orders/order-actions";
import {
  createOrderIntent,
  loadOrderIntent,
  resolveOrderAttempt,
  saveOrderIntent,
  sendOrderIntent,
} from "../../src/app/dashboard/orders/order-intent";
import { orderDetailSchema } from "../../src/contracts/staff-orders";
import { envelope, orderDetail } from "../helpers/pickup-fixtures";

const order = orderDetailSchema.parse(envelope(orderDetail)).data;
it("renders only server-offered actions without a frontend transition-policy matrix", () => {
  const tree = ActionOptions({ actions: ["mark_paid"] });
  const options = Children.toArray(tree.props.children).filter(
    isValidElement<{ value: string; children: ReactNode }>,
  );
  expect(options.map((option) => option.props.value)).toEqual(["mark_paid"]);
  expect(options.map((option) => option.props.children)).toEqual([
    "Record payment",
  ]);
});
it("retains exact key/body across an uncertain POST and manual reconciliation, without automatic requests", async () => {
  const intent = createOrderIntent(order, "accept", "synthetic-intent-key");
  const values = new Map<string, string>();
  const storage = {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => {
      values.set(key, value);
    },
    removeItem: (key: string) => {
      values.delete(key);
    },
  };
  saveOrderIntent(storage, intent);
  const restored = loadOrderIntent(storage, order.id);
  expect(restored).toEqual(intent);
  let calls = 0;
  const outbound: RequestInit[] = [];
  const sender = async (_input: RequestInfo | URL, init?: RequestInit) => {
    calls++;
    if (init) outbound.push(init);
    if (calls === 1) throw new Error("synthetic disconnect");
    return Response.json(
      envelope({
        ...order,
        version: "1",
        fulfillment_status: "accepted",
        permitted_actions: ["start_preparing"],
      }),
    );
  };
  expect(await sendOrderIntent(intent, sender)).toEqual({
    kind: "uncertain",
    code: "temporarily_unavailable",
  });
  expect(calls).toBe(1);
  expect(loadOrderIntent(storage, order.id)).toEqual(intent);
  const result = await sendOrderIntent(intent, sender);
  expect(result.kind).toBe("confirmed");
  expect(calls).toBe(2);
  expect(outbound.map((value) => value.body)).toEqual([
    intent.body,
    intent.body,
  ]);
  expect(
    outbound.map((value) => new Headers(value.headers).get("idempotency-key")),
  ).toEqual([intent.key, intent.key]);
  expect(order.version).toBe("0");
});
it("treats stale original replay as a committed result, not a new current snapshot; errors/5xx remain distinct", async () => {
  const known = { ...order, version: "8" };
  const intent = createOrderIntent(order, "accept", "same-key");
  const result = await sendOrderIntent(intent, async () =>
    Response.json(envelope({ ...order, version: "1" })),
  );
  expect(result.kind === "confirmed" && result.order.version).toBe("1");
  expect(known.version).toBe("8");
  const resolution = resolveOrderAttempt(result, true);
  expect(resolution.kind).toBe("review");
  expect(Object.keys(resolution).sort()).toEqual(["kind", "message"]);
  expect(resolution.message).toContain("may not be the current version");
  expect(
    resolveOrderAttempt({ kind: "rejected", code: "version_conflict" }, true)
      .kind,
  ).toBe("uncertain");
  expect(
    resolveOrderAttempt({ kind: "rejected", code: "unauthenticated" }, true)
      .kind,
  ).toBe("uncertain");
  expect(
    resolveOrderAttempt({ kind: "rejected", code: "version_conflict" }, false)
      .kind,
  ).toBe("review");
  for (const [status, code, kind] of [
    [409, "version_conflict", "rejected"],
    [422, "policy_unresolved", "rejected"],
    [503, "temporarily_unavailable", "uncertain"],
    [500, "internal_error", "uncertain"],
  ] as const) {
    expect(
      await sendOrderIntent(intent, async () =>
        Response.json(
          {
            error: { code, message: "SYNTHETIC_UNSAFE_TEXT" },
            request_id: "a".repeat(32),
          },
          { status },
        ),
      ),
    ).toEqual({ kind, code });
  }
  expect(
    await sendOrderIntent(intent, async () => new Response("not json")),
  ).toEqual({ kind: "uncertain", code: "temporarily_unavailable" });
});
it("rejects corrupt or foreign saved intents instead of silently inventing a new key", () => {
  const storage = {
    getItem: () =>
      JSON.stringify({
        order_id: order.id,
        key: "bad key",
        body: '{"action":"accept","expected_version":"0"}',
      }),
  };
  expect(() => loadOrderIntent(storage, order.id)).toThrow();
});
