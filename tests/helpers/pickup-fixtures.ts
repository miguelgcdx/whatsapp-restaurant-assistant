// Synthetic protocol values, not a local setup/session or an operational JWT.
export const orderId = "11111111-1111-4111-8111-111111111111";
export const summaryId = "22222222-2222-4222-8222-222222222222";
export const money = { amount_minor: "1600", currency: "USD", exponent: 2 };
export const orderSummary = {
  id: orderId,
  version: "0",
  created_at: "2026-10-30T12:00:00Z",
  fulfillment_status: "submitted",
  payment_status: "unpaid",
  fulfillment_kind: "pickup",
  total: money,
};
export const orderDetail = {
  ...orderSummary,
  permitted_actions: ["accept", "reject", "cancel", "mark_paid"],
  confirmed: {
    id: summaryId,
    unit_count: 1,
    units_href: `/api/staff/orders/${orderId}/summaries/${summaryId}/units`,
    fulfillment: { kind: "pickup", payment_method: "pay_at_pickup" },
    subtotal: money,
    delivery_fee: { ...money, amount_minor: "0" },
    total: money,
    fee_basis: "independent",
    fee_source_order_id: null,
  },
  payment: { state: "unpaid", collected_at: null },
  estimate: null,
  preparation_started_at: null,
};
export const unitPage = {
  items: [
    {
      id: "33333333-3333-4333-8333-333333333333",
      menu_item_id: "44444444-4444-4444-8444-444444444444",
      name: "Garden bowl",
      note: "No coriander",
      unit_price: money,
    },
  ],
  next_cursor: null,
  summary_id: summaryId,
  unit_count: 1,
};
export const envelope = <T>(data: T) => ({ data, request_id: "a".repeat(32) });
