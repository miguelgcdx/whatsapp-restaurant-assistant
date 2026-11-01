import "server-only";
import { redirect } from "next/navigation";
import {
  orderDetailSchema,
  orderErrorSchema,
  orderIdSchema,
  orderListSchema,
  ordersQuery,
  orderUnitsSchema,
} from "@/contracts/staff-orders";
import { runtimeAuth } from "@/server/auth/runtime";
import {
  type OrderReadTarget,
  staffOrdersRequest,
} from "@/server/staff-orders";

export type OrderSearch = Record<string, string | string[] | undefined>;
const unavailable = {
  kind: "error",
  status: 503,
  message: "Orders are temporarily unavailable. Try again later.",
} as const;
const invalid = {
  kind: "error",
  status: 400,
  message: "Invalid order request.",
} as const;

/** Request-local only: no self-HTTP, private prefetch, or cached user data. */
export async function loadOrders(
  headers: Headers,
  target: OrderReadTarget,
  search: OrderSearch,
) {
  let query: URLSearchParams;
  try {
    const raw = new URLSearchParams();
    for (const [key, value] of Object.entries(search)) {
      if (Array.isArray(value) || value === undefined) return invalid;
      raw.append(key, value);
    }
    query = ordersQuery(raw.toString());
    if (target.kind === "detail" && query.size) return invalid;
    if (target.kind !== "list") orderIdSchema.parse(target.id);
    if (target.kind === "units") orderIdSchema.parse(target.summaryId);
  } catch {
    return invalid;
  }
  const config = await runtimeAuth().catch(() => null);
  if (!config) return unavailable;
  let response: Response;
  let value: unknown;
  try {
    const path =
      target.kind === "list"
        ? "/api/staff/orders"
        : target.kind === "detail"
          ? `/api/staff/orders/${target.id}`
          : `/api/staff/orders/${target.id}/summaries/${target.summaryId}/units`;
    response = await staffOrdersRequest(
      new Request(`${config.origin}${path}${query.size ? `?${query}` : ""}`, {
        headers,
      }),
      target,
      config,
    );
    value = await response.json();
  } catch {
    return unavailable;
  }
  if (response.status !== 200) {
    const error = orderErrorSchema.safeParse(value);
    if (!error.success) return unavailable;
    if (response.status === 401 && error.data.error.code === "unauthenticated")
      redirect("/sign-in");
    if (response.status === 403 && error.data.error.code === "forbidden")
      redirect("/access-denied");
    if (response.status === 404 && error.data.error.code === "not_found")
      return {
        kind: "error",
        status: 404,
        message: "Order or confirmed summary was not found.",
      } as const;
    if (response.status === 400 && error.data.error.code === "invalid_input")
      return invalid;
    return unavailable;
  }
  const limit = query.get("limit") ?? undefined;
  if (target.kind === "list") {
    const parsed = orderListSchema.safeParse(value);
    return parsed.success
      ? { kind: "list" as const, data: parsed.data.data, limit }
      : unavailable;
  }
  if (target.kind === "detail") {
    const parsed = orderDetailSchema.safeParse(value);
    return parsed.success && parsed.data.data.id === target.id
      ? { kind: "detail" as const, data: parsed.data.data }
      : unavailable;
  }
  const parsed = orderUnitsSchema.safeParse(value);
  return parsed.success && parsed.data.data.summary_id === target.summaryId
    ? { kind: "units" as const, data: parsed.data.data, limit }
    : unavailable;
}
