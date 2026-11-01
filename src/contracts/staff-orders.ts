import { z } from "zod";
import { catalogSchema, formatPrice, priceSchema } from "./staff-catalog";

export const orderIdSchema =
  catalogSchema.shape.data.shape.items.element.shape.id;
export const orderVersionSchema =
  catalogSchema.shape.data.shape.items.element.shape.version;
const requestId = catalogSchema.shape.request_id;
const time = z.string().datetime();
const cursor = z
  .string()
  .min(1)
  .max(2048)
  .refine((value) =>
    [...value].every((character) => character.charCodeAt(0) <= 127),
  );
export const orderActions = [
  "accept",
  "reject",
  "start_preparing",
  "mark_ready",
  "mark_picked_up",
  "cancel",
  "mark_paid",
] as const;
export const orderSummarySchema = z.strictObject({
  id: orderIdSchema,
  version: orderVersionSchema,
  created_at: time,
  fulfillment_status: z.enum([
    "submitted",
    "accepted",
    "preparing",
    "ready",
    "picked_up",
    "rejected",
    "cancelled",
  ]),
  payment_status: z.enum(["unpaid", "paid"]),
  fulfillment_kind: z.literal("pickup"),
  total: priceSchema,
});
export const orderDetailDataSchema = orderSummarySchema
  .extend({
    permitted_actions: z
      .array(z.enum(orderActions))
      .refine((actions) => new Set(actions).size === actions.length),
    confirmed: z.strictObject({
      id: orderIdSchema,
      unit_count: z.number().int().nonnegative(),
      units_href: z.string(),
      fulfillment: z.strictObject({
        kind: z.literal("pickup"),
        payment_method: z.literal("pay_at_pickup"),
      }),
      subtotal: priceSchema,
      delivery_fee: priceSchema,
      total: priceSchema,
      fee_basis: z.literal("independent"),
      fee_source_order_id: z.null(),
    }),
    payment: z.strictObject({
      state: z.enum(["unpaid", "paid"]),
      collected_at: time.nullable(),
    }),
    estimate: z
      .strictObject({
        minutes: z.number().int().nonnegative(),
        as_of: time,
        provisional: z.boolean(),
      })
      .nullable(),
    preparation_started_at: time.nullable(),
  })
  .refine(
    (value) =>
      value.confirmed.units_href ===
        `/api/staff/orders/${value.id}/summaries/${value.confirmed.id}/units` &&
      value.payment.state === value.payment_status,
  );
export const orderListSchema = z.strictObject({
  data: z.strictObject({
    items: z.array(orderSummarySchema).max(100),
    next_cursor: cursor.nullable(),
  }),
  request_id: requestId,
});
export const orderDetailSchema = z.strictObject({
  data: orderDetailDataSchema,
  request_id: requestId,
});
export const orderUnitsSchema = z.strictObject({
  data: z.strictObject({
    items: z
      .array(
        z.strictObject({
          id: orderIdSchema,
          menu_item_id: orderIdSchema,
          name: z.string(),
          note: z.string(),
          unit_price: priceSchema,
        }),
      )
      .max(100),
    next_cursor: cursor.nullable(),
    summary_id: orderIdSchema,
    unit_count: z.number().int().nonnegative(),
  }),
  request_id: requestId,
});
export type OrderDetail = z.infer<typeof orderDetailDataSchema>;
export type OrderSummary = z.infer<typeof orderSummarySchema>;
export type OrderAction = (typeof orderActions)[number];
export const formatOrderMoney = formatPrice;
export function ordersQuery(raw: string): URLSearchParams {
  if (raw === "?") throw new Error("Invalid query");
  const source = raw.startsWith("?") ? raw.slice(1) : raw;
  if (source)
    for (const part of source.split("&")) {
      if (!part || part.includes(";")) throw new Error("Invalid query");
      decodeURIComponent(part.replaceAll("+", " "));
    }
  const query = new URLSearchParams(source);
  const result = new URLSearchParams();
  for (const [key, value] of query) {
    if (result.has(key)) throw new Error("Duplicate query");
    if (key === "limit") {
      if (!/^(?:[1-9][0-9]?|100)$/.test(value))
        throw new Error("Invalid limit");
    } else if (key === "cursor") cursor.parse(value);
    else throw new Error("Unknown query");
    result.set(key, value);
  }
  return result;
}
const intent = z.strictObject({
  action: z.enum(orderActions),
  expected_version: orderVersionSchema,
});
// Flat two-string protocol: reject duplicate keys before JSON.parse discards them.
const stringToken =
  '"(?:[^"\\\\\\u0000-\\u001f]|\\\\(?:["\\\\/bfnrt]|u[0-9a-fA-F]{4}))*"';
const pair = new RegExp(
  `^\\s*\\{\\s*(${stringToken})\\s*:\\s*${stringToken}\\s*,\\s*(${stringToken})\\s*:\\s*${stringToken}\\s*\\}\\s*$`,
);
export function orderActionBody(text: string) {
  const match = pair.exec(text);
  if (!match || JSON.parse(match[1]) === JSON.parse(match[2]))
    throw new Error("Invalid action");
  const value: unknown = JSON.parse(text);
  return intent.parse(value);
}
export const orderErrors = {
  invalid_input: { status: 400, message: "Invalid order request." },
  unauthenticated: { status: 401, message: "Authentication is required." },
  forbidden: { status: 403, message: "Staff access is not available." },
  not_found: {
    status: 404,
    message: "Order or confirmed summary was not found.",
  },
  method_not_allowed: { status: 405, message: "The method is not allowed." },
  version_conflict: {
    status: 409,
    message: "The order changed. Reload before choosing another action.",
  },
  invalid_transition: {
    status: 409,
    message: "This action is not available for the current order state.",
  },
  idempotency_conflict: {
    status: 409,
    message:
      "This request key was used for another intent. Do not retry a changed request with it.",
  },
  payload_too_large: { status: 413, message: "Request body is too large." },
  unsupported_media_type: {
    status: 415,
    message: "Content-Type must be application/json.",
  },
  policy_unresolved: {
    status: 422,
    message:
      "This action requires a policy not available in this pickup slice.",
  },
  temporarily_unavailable: {
    status: 503,
    message:
      "Orders are temporarily unavailable. An action result may be uncertain; do not submit a new intent automatically.",
  },
  internal_error: {
    status: 500,
    message:
      "The order action could not be completed. Its result may be uncertain.",
  },
} as const;
export type OrderErrorCode = keyof typeof orderErrors;
const codes = z.enum([
  "invalid_input",
  "unauthenticated",
  "forbidden",
  "not_found",
  "method_not_allowed",
  "version_conflict",
  "invalid_transition",
  "idempotency_conflict",
  "payload_too_large",
  "unsupported_media_type",
  "policy_unresolved",
  "temporarily_unavailable",
  "internal_error",
]);
export const orderErrorSchema = z.strictObject({
  error: z.strictObject({ code: codes, message: z.string() }),
  request_id: requestId,
});
