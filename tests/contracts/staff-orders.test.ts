import { expect, it } from "vitest";
import {
  orderActionBody,
  orderDetailSchema,
  orderListSchema,
  ordersQuery,
  orderUnitsSchema,
} from "../../src/contracts/staff-orders";
import {
  envelope,
  orderDetail,
  orderSummary,
  unitPage,
} from "../helpers/pickup-fixtures";

const minimalHeader = {
  id: orderSummary.id,
  version: orderSummary.version,
  created_at: orderSummary.created_at,
  fulfillment_status: orderSummary.fulfillment_status,
  payment_status: orderSummary.payment_status,
  fulfillment_kind: orderSummary.fulfillment_kind,
  total: orderSummary.total,
};
it("keeps the r3 list minimal, without widening snapshot or action fields", () => {
  expect(
    orderListSchema.safeParse(
      envelope({ items: [minimalHeader], next_cursor: null }),
    ).success,
  ).toBe(true);
  for (const extra of [
    { permitted_actions: [] },
    { confirmed_summary_id: orderDetail.confirmed.id },
  ]) {
    expect(
      orderListSchema.safeParse(
        envelope({
          items: [{ ...minimalHeader, ...extra }],
          next_cursor: null,
        }),
      ).success,
    ).toBe(false);
  }
});
it("requires unique r3 capabilities only on detail/action and anchors units at confirmed.id", () => {
  const detail = {
    ...minimalHeader,
    confirmed: orderDetail.confirmed,
    payment: orderDetail.payment,
    estimate: orderDetail.estimate,
    preparation_started_at: orderDetail.preparation_started_at,
    permitted_actions: ["accept", "reject"],
  };
  expect(orderDetailSchema.safeParse(envelope(detail)).success).toBe(true);
  expect(
    orderDetailSchema.safeParse(envelope({ ...detail, permitted_actions: [] }))
      .success,
  ).toBe(true);
  for (const permitted_actions of [
    undefined,
    null,
    ["accept", "accept"],
    ["deliver"],
  ]) {
    expect(
      orderDetailSchema.safeParse(envelope({ ...detail, permitted_actions }))
        .success,
    ).toBe(false);
  }
  expect(
    orderDetailSchema.safeParse(
      envelope({ ...detail, confirmed_summary_id: detail.confirmed.id }),
    ).success,
  ).toBe(false);
  expect(
    orderDetailSchema.safeParse(
      envelope({
        ...detail,
        confirmed: { ...detail.confirmed, id: detail.id },
      }),
    ).success,
  ).toBe(false);
});
it("parses the selected pickup snapshot without inventing readiness or prices", () => {
  expect(
    orderListSchema.parse(
      envelope({ items: [orderSummary], next_cursor: null }),
    ).data.items[0]?.total.amount_minor,
  ).toBe("1600");
  expect(
    orderDetailSchema.parse(envelope(orderDetail)).data.estimate,
  ).toBeNull();
  expect(orderUnitsSchema.parse(envelope(unitPage)).data.items[0]?.note).toBe(
    "No coriander",
  );
  for (const version of ["01", "-1", "9223372036854775808"]) {
    expect(
      orderDetailSchema.safeParse(envelope({ ...orderDetail, version }))
        .success,
    ).toBe(false);
  }
  expect(
    orderDetailSchema.safeParse(
      envelope({
        ...orderDetail,
        confirmed: {
          ...orderDetail.confirmed,
          units_href: "https://foreign.invalid/units",
        },
      }),
    ).success,
  ).toBe(false);
});
it("accepts only bounded cursor queries and the exact two-field action intent", () => {
  expect(ordersQuery("?limit=100&cursor=opaque").toString()).toBe(
    "limit=100&cursor=opaque",
  );
  for (const query of [
    "?",
    "?limit=0",
    "?limit=101",
    "?limit=1&limit=2",
    "?cursor=",
    "?restaurant_id=x",
    "?cursor=%FF",
  ]) {
    expect(() => ordersQuery(query)).toThrow();
  }
  expect(orderActionBody('{"action":"accept","expected_version":"0"}')).toEqual(
    { action: "accept", expected_version: "0" },
  );
  for (const body of [
    '{"action":"accept","action":"reject"}',
    '{"action":"accept","expected_version":"0","price":"0"}',
    '{"action":"deliver","expected_version":"0"}',
    '{"action":"accept","expected_version":0}',
  ])
    expect(() => orderActionBody(body)).toThrow();
});
