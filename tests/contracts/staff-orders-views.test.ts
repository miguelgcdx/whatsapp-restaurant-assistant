import Link from "next/link";
import { Children, isValidElement, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import OrderPage from "../../src/app/dashboard/orders/[id]/page";
import { OrderActions } from "../../src/app/dashboard/orders/order-actions";
import {
  orderDetailSchema,
  orderListSchema,
  orderUnitsSchema,
} from "../../src/contracts/staff-orders";
import {
  envelope,
  orderDetail,
  orderId,
  orderSummary,
  summaryId,
  unitPage,
} from "../helpers/pickup-fixtures";

const boundary = vi.hoisted(() => ({ request: vi.fn(), auth: vi.fn() }));
vi.mock("server-only", () => ({}));
vi.mock("next/headers", () => ({ headers: async () => new Headers() }));
vi.mock("../../src/server/staff-orders", () => ({
  staffOrdersRequest: boundary.request,
}));
vi.mock("../../src/server/auth/runtime", () => ({
  runtimeAuth: boundary.auth,
}));
vi.mock("next/navigation", () => ({
  redirect: (path: string) => {
    throw new Error(`redirect:${path}`);
  },
}));

import { loadOrders } from "../../src/app/dashboard/orders/order-load";
import {
  OrderDetailView,
  OrderListView,
  OrderReadError,
  OrderUnitsView,
} from "../../src/app/dashboard/orders/order-views";

function elements(node: ReactNode): {
  type: unknown;
  props: { children?: ReactNode; href?: string; prefetch?: boolean };
}[] {
  return Children.toArray(node).flatMap((child) =>
    isValidElement<{ children?: ReactNode; href?: string; prefetch?: boolean }>(
      child,
    )
      ? [child, ...elements(child.props.children)]
      : [],
  );
}
function text(node: ReactNode): string {
  return Children.toArray(node)
    .map((child) =>
      isValidElement<{ children?: ReactNode }>(child)
        ? text(child.props.children)
        : String(child),
    )
    .join("");
}

it("joins the authenticated detail projection with the action panel for that exact order", async () => {
  boundary.auth
    .mockReset()
    .mockResolvedValue({ origin: "http://fixture.invalid" });
  boundary.request
    .mockReset()
    .mockResolvedValue(Response.json(envelope(orderDetail)));
  const page = await OrderPage({
    params: Promise.resolve({ id: orderId }),
    searchParams: Promise.resolve({}),
  });
  expect(page.type).toBe(OrderDetailView);
  expect(isValidElement(page.props.actions)).toBe(true);
  if (!isValidElement<{ order: unknown }>(page.props.actions))
    throw new Error("Missing action panel");
  expect(page.props.actions.type).toBe(OrderActions);
  expect(page.props.actions.key).toBe(orderId);
  expect(page.props.actions.props.order).toEqual(
    orderDetailSchema.parse(envelope(orderDetail)).data,
  );
});
describe("staff order read views (structural, not browser acceptance)", () => {
  it("distinguishes an empty page and uses exact server money/status on a populated page", () => {
    expect(
      text(
        OrderListView({
          data: orderListSchema.parse(
            envelope({ items: [], next_cursor: null }),
          ).data,
        }),
      ),
    ).toContain("No orders on this page.");
    const tree = OrderListView({
      data: orderListSchema.parse(
        envelope({ items: [orderSummary], next_cursor: "next+ /" }),
      ).data,
      limit: "1",
    });
    expect(text(tree)).toContain("USD 16.00");
    expect(text(tree)).toContain("Fulfillment: submitted");
    expect(text(tree)).toContain("Payment: unpaid");
    const links = elements(tree).filter((node) => node.props.href);
    expect(links.map((node) => node.props.href)).toEqual([
      `/dashboard/orders/${orderId}`,
      "/dashboard/orders?limit=1&cursor=next%2B+%2F",
    ]);
    expect(
      links.every(
        (node) => node.type === Link && node.props.prefetch === false,
      ),
    ).toBe(true);
  });

  it("renders immutable totals, separate payment, and null preparation estimate without guessing", () => {
    const tree = OrderDetailView({
      order: orderDetailSchema.parse(envelope(orderDetail)).data,
    });
    const output = text(tree);
    for (const exact of [
      "Subtotal: USD 16.00",
      "Delivery fee: USD 0.00",
      "Confirmed total: USD 16.00",
      "Payment: unpaid",
      "Fulfillment: submitted",
      "Preparation estimate: Not provided",
      "Preparation started: Not provided",
      "Collection time: Not provided",
    ])
      expect(output).toContain(exact);
    expect(
      elements(tree)
        .filter((node) => node.props.href)
        .map((node) => node.props.href),
    ).toEqual([
      `/dashboard/orders/${orderId}/summaries/${summaryId}/units`,
      "/dashboard/orders",
    ]);
  });

  it("labels provisional and nonprovisional estimates with exact as_of and preparation-only meaning, independently of paid fulfillment", () => {
    for (const provisional of [true, false]) {
      const order = orderDetailSchema.parse(
        envelope({
          ...orderDetail,
          payment_status: "paid",
          payment: { state: "paid", collected_at: "2026-10-30T12:01:00Z" },
          estimate: { minutes: 0, as_of: "2026-10-30T12:00:00Z", provisional },
        }),
      ).data;
      const output = text(OrderDetailView({ order }));
      for (const exact of [
        "Payment: paid",
        "Fulfillment: submitted",
        "0 minutes",
        "Preparation only",
        "As of: 2026-10-30T12:00:00Z",
        `Provisional: ${provisional ? "yes" : "no"}`,
        "Collection time: 2026-10-30T12:01:00Z",
      ])
        expect(output).toContain(exact);
    }
  });

  it("preserves exact unit names/notes and pins pagination to the same immutable summary", () => {
    const data = orderUnitsSchema.parse(
      envelope({ ...unitPage, next_cursor: "opaque+/=" }),
    ).data;
    const tree = OrderUnitsView({ id: orderId, data, limit: "1" });
    expect(text(tree)).toContain("Garden bowl");
    expect(text(tree)).toContain("No coriander");
    expect(text(tree)).toContain("USD 16.00");
    expect(text(tree)).toContain("1 units in confirmed summary");
    expect(
      elements(tree)
        .filter((node) => node.props.href)
        .map((node) => node.props.href),
    ).toEqual([
      `/dashboard/orders/${orderId}/summaries/${summaryId}/units?limit=1&cursor=opaque%2B%2F%3D`,
      `/dashboard/orders/${orderId}`,
    ]);
  });
});

describe("localized server loader", () => {
  it.each([
    { limit: ["1", "2"] },
    { limit: ["1"] },
    { unknown: "x" },
    { cursor: "" },
    { limit: "101" },
    { limit: "0" },
    { limit: "01" },
    { limit: undefined },
    { cursor: "é" },
    { cursor: "x".repeat(2049) },
    { cursor: ["first", "second"] },
  ])("rejects invalid query without auth/upstream or data changes: %j", async (query) => {
    boundary.auth.mockClear();
    boundary.request.mockClear();
    const before = JSON.stringify(orderDetail);
    expect(await loadOrders(new Headers(), { kind: "list" }, query)).toEqual({
      kind: "error",
      status: 400,
      message: "Invalid order request.",
    });
    expect(boundary.auth).not.toHaveBeenCalled();
    expect(boundary.request).not.toHaveBeenCalled();
    expect(JSON.stringify(orderDetail)).toBe(before);
  });
  it("rejects malformed identifiers and detail queries before upstream", async () => {
    boundary.request.mockClear();
    for (const target of [
      { kind: "detail", id: "bad" },
      { kind: "units", id: orderId, summaryId: "bad" },
      { kind: "detail", id: orderId },
    ] as const) {
      expect(
        await loadOrders(
          new Headers(),
          target,
          target.id === "bad" || target.kind === "units" ? {} : { limit: "1" },
        ),
      ).toEqual({
        kind: "error",
        status: 400,
        message: "Invalid order request.",
      });
    }
    expect(boundary.request).not.toHaveBeenCalled();
  });
  it("uses the common boundary directly and parses its typed envelope", async () => {
    boundary.auth.mockResolvedValue({ origin: "http://fixture.invalid" });
    boundary.request.mockResolvedValue(
      Response.json(envelope({ items: [orderSummary], next_cursor: null })),
    );
    const result = await loadOrders(
      new Headers(),
      { kind: "list" },
      { limit: "1", cursor: "opaque+" },
    );
    expect(result).toEqual({
      kind: "list",
      data: orderListSchema.parse(
        envelope({ items: [orderSummary], next_cursor: null }),
      ).data,
      limit: "1",
    });
    expect(boundary.request).toHaveBeenCalledTimes(1);
    expect(boundary.request.mock.calls[0]?.[0].url).toBe(
      "http://fixture.invalid/api/staff/orders?limit=1&cursor=opaque%2B",
    );
  });
  it.each([
    [401, "unauthenticated", "/sign-in"],
    [403, "forbidden", "/access-denied"],
  ] as const)("redirects %s", async (status, code, path) => {
    boundary.request.mockResolvedValue(
      Response.json(
        { error: { code, message: "ignored" }, request_id: "a".repeat(32) },
        { status },
      ),
    );
    await expect(
      loadOrders(new Headers(), { kind: "list" }, {}),
    ).rejects.toThrow(`redirect:${path}`);
  });
  it.each([
    [404, "not_found", "Order or confirmed summary was not found."],
    [
      503,
      "temporarily_unavailable",
      "Orders are temporarily unavailable. Try again later.",
    ],
  ] as const)("shows a safe %s state", async (status, code, message) => {
    boundary.request.mockResolvedValue(
      Response.json(
        {
          error: { code, message: "private upstream detail" },
          request_id: "a".repeat(32),
        },
        { status },
      ),
    );
    expect(await loadOrders(new Headers(), { kind: "list" }, {})).toEqual({
      kind: "error",
      status,
      message,
    });
  });
  it("parses detail and immutable units through the same boundary", async () => {
    boundary.request.mockResolvedValueOnce(
      Response.json(envelope(orderDetail)),
    );
    expect(
      await loadOrders(new Headers(), { kind: "detail", id: orderId }, {}),
    ).toEqual({
      kind: "detail",
      data: orderDetailSchema.parse(envelope(orderDetail)).data,
    });
    boundary.request.mockResolvedValueOnce(Response.json(envelope(unitPage)));
    expect(
      await loadOrders(
        new Headers(),
        { kind: "units", id: orderId, summaryId },
        {},
      ),
    ).toEqual({
      kind: "units",
      data: orderUnitsSchema.parse(envelope(unitPage)).data,
      limit: undefined,
    });
    expect(
      text(
        OrderReadError({
          status: 404,
          message: "Order or confirmed summary was not found.",
        }),
      ),
    ).toBe(
      "Staff orders404: Order or confirmed summary was not found.Start again",
    );
  });
  it("keeps unit whitespace exact and shows an explicit empty units page", () => {
    const note = "  No coriander\nKeep separate  ";
    const name = "Garden bowl <special>";
    const data = orderUnitsSchema.parse(
      envelope({ ...unitPage, items: [{ ...unitPage.items[0], name, note }] }),
    ).data;
    const tree = OrderUnitsView({ id: orderId, data });
    expect(
      elements(tree).some(
        (node) => node.type === "h2" && node.props.children === name,
      ),
    ).toBe(true);
    expect(
      elements(tree).some(
        (node) => node.type === "p" && node.props.children === note,
      ),
    ).toBe(true);
    expect(
      text(OrderUnitsView({ id: orderId, data: { ...data, items: [] } })),
    ).toContain("No units on this page.");
  });
  it("shows unavailable configuration without touching the common boundary", async () => {
    boundary.auth.mockRejectedValueOnce(
      new Error("synthetic dependency unavailable"),
    );
    boundary.request.mockClear();
    expect(await loadOrders(new Headers(), { kind: "list" }, {})).toEqual({
      kind: "error",
      status: 503,
      message: "Orders are temporarily unavailable. Try again later.",
    });
    expect(boundary.request).not.toHaveBeenCalled();
  });
  it("never invents empty data for malformed success", async () => {
    boundary.request.mockResolvedValue(Response.json({ data: {} }));
    expect(await loadOrders(new Headers(), { kind: "list" }, {})).toEqual({
      kind: "error",
      status: 503,
      message: "Orders are temporarily unavailable. Try again later.",
    });
  });
});
