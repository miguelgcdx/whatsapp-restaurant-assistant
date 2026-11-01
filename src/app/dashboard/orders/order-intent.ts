import { z } from "zod";
import {
  type OrderAction,
  type OrderDetail,
  type OrderErrorCode,
  orderActionBody,
  orderDetailSchema,
  orderErrorSchema,
  orderErrors,
  orderIdSchema,
} from "@/contracts/staff-orders";

const stored = z.strictObject({
  order_id: orderIdSchema,
  key: z.string().regex(/^[\x21-\x7e]{1,128}$/),
  body: z.string().max(65536),
});
export type OrderIntent = Readonly<z.infer<typeof stored>>;
type IntentStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;
export type OrderAttempt =
  | { kind: "confirmed"; order: OrderDetail }
  | { kind: "rejected" | "uncertain"; code: OrderErrorCode };
export function resolveOrderAttempt(
  result: OrderAttempt,
  earlierUncertain: boolean,
):
  | { kind: "review"; message: string }
  | { kind: "uncertain"; message: string } {
  if (result.kind === "confirmed")
    return {
      kind: "review",
      message: `Committed result received at version ${result.order.version}. It may not be the current version. Reload before another intent.`,
    };
  const message = orderErrors[result.code].message;
  return earlierUncertain || result.kind === "uncertain"
    ? {
        kind: "uncertain",
        message: message + " An earlier attempt may still have committed.",
      }
    : { kind: "review", message: message + " Reload before another intent." };
}
export const orderIntentKey = (id: string) => `staff-pickup-intent:${id}`;
export function createOrderIntent(
  order: Pick<OrderDetail, "id" | "version">,
  action: OrderAction,
  key: string,
): OrderIntent {
  const body = JSON.stringify(
    orderActionBody(
      JSON.stringify({ action, expected_version: order.version }),
    ),
  );
  return Object.freeze(stored.parse({ order_id: order.id, key, body }));
}
export function saveOrderIntent(storage: IntentStorage, intent: OrderIntent) {
  storage.setItem(
    orderIntentKey(intent.order_id),
    JSON.stringify(stored.parse(intent)),
  );
}
export function loadOrderIntent(
  storage: Pick<Storage, "getItem">,
  id: string,
): OrderIntent | null {
  const text = storage.getItem(orderIntentKey(id));
  if (text === null) return null;
  const value: unknown = JSON.parse(text);
  const intent = stored.parse(value);
  if (intent.order_id !== id) throw new Error("Saved intent identity");
  orderActionBody(intent.body);
  return Object.freeze(intent);
}
export async function sendOrderIntent(
  intent: OrderIntent,
  sender: typeof fetch = fetch,
): Promise<OrderAttempt> {
  try {
    stored.parse(intent);
    orderActionBody(intent.body);
    const response = await sender(
      `/api/staff/orders/${intent.order_id}/actions`,
      {
        method: "POST",
        credentials: "same-origin",
        redirect: "error",
        cache: "no-store",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": intent.key,
        },
        body: intent.body,
        signal: AbortSignal.timeout(5000),
      },
    );
    const value: unknown = await response.json();
    if (response.status === 200) {
      const result = orderDetailSchema.safeParse(value);
      if (result.success && result.data.data.id === intent.order_id)
        return { kind: "confirmed", order: result.data.data };
    } else {
      const result = orderErrorSchema.safeParse(value);
      if (result.success) {
        const { code } = result.data.error;
        const expected = orderErrors[code].status;
        if (response.status === expected)
          return {
            kind: response.status >= 500 ? "uncertain" : "rejected",
            code,
          };
      }
    }
  } catch {
    /* A missing acknowledgement is not proof of rejection. */
  }
  return { kind: "uncertain", code: "temporarily_unavailable" };
}
