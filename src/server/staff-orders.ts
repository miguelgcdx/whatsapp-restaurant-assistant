import "server-only";
import { randomBytes } from "node:crypto";
import { request as httpRequest } from "node:http";
import {
  type OrderErrorCode,
  orderDetailSchema,
  orderErrorSchema,
  orderErrors,
  orderIdSchema,
  orderListSchema,
  ordersQuery,
  orderUnitsSchema,
} from "../contracts/staff-orders";
import { localOrigin, originAllowed } from "./auth/config";
import type { StaffAuth } from "./auth/factory";
import { authoritativeSession } from "./auth/session";

export type OrderConfig = {
  auth: StaffAuth;
  origin: string;
  apiOrigin: string;
};
export type OrderReadTarget =
  | { kind: "list" }
  | { kind: "detail"; id: string }
  | { kind: "units"; id: string; summaryId: string };
export function orderError(
  code: OrderErrorCode,
  allow: "GET" | "POST" = "GET",
  requestId = randomBytes(16).toString("hex"),
) {
  const error = orderErrors[code];
  return Response.json(
    { error: { code, message: error.message }, request_id: requestId },
    {
      status: error.status,
      headers: {
        "Cache-Control": "no-store",
        ...(code === "method_not_allowed" ? { Allow: allow } : {}),
      },
    },
  );
}
export async function orderAccess(
  headers: Headers,
  config: OrderConfig,
  mutation = false,
): Promise<{ token: string } | Response> {
  try {
    if (!originAllowed(headers, config.origin, mutation))
      return orderError("forbidden");
    if (!(await authoritativeSession(config.auth, headers)))
      return orderError("unauthenticated");
    return await config.auth.api.getToken({ headers });
  } catch {
    return orderError("temporarily_unavailable");
  }
}
type GoInput = { path: string; token: string } & (
  | { method: "GET" }
  | { method: "POST"; body: Buffer; key: string }
);
/** Only callers in the order boundary build paths; never forward browser authority. */
export function orderGo(
  config: OrderConfig,
  input: GoInput,
): Promise<{ status: number; raw: Buffer; value: unknown }> {
  const origin = localOrigin(config.apiOrigin);
  return new Promise((resolve, reject) => {
    const request = httpRequest(
      `${origin}${input.path}`,
      {
        method: input.method,
        agent: false,
        headers: {
          Accept: "application/json",
          Authorization: `Bearer ${input.token}`,
          ...(input.method === "POST"
            ? {
                "Content-Type": "application/json",
                "Content-Length": input.body.length,
                "Idempotency-Key": input.key,
              }
            : {}),
        },
      },
      (response) => {
        const chunks: Buffer[] = [];
        let size = 0;
        response.on("data", (chunk: Buffer) => {
          size += chunk.length;
          if (size > 512 * 1024) {
            response.destroy();
            request.destroy(new Error("Bounded response"));
            return;
          }
          chunks.push(chunk);
        });
        response.once("error", reject);
        response.once("aborted", () =>
          reject(new Error("Incomplete response")),
        );
        response.once("end", () => {
          try {
            const raw = Buffer.concat(chunks);
            const value: unknown = JSON.parse(
              new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(
                raw,
              ),
            );
            resolve({ status: response.statusCode ?? 503, raw, value });
          } catch {
            reject(new Error("Invalid response"));
          }
        });
      },
    );
    const deadline = setTimeout(
      () => request.destroy(new Error("Dependency deadline")),
      2000,
    );
    request.once("close", () => clearTimeout(deadline));
    request.once("error", reject);
    request.end(input.method === "POST" ? input.body : undefined);
  });
}
export function orderGoError(
  status: number,
  value: unknown,
  allow: "GET" | "POST" = "GET",
) {
  const parsed = orderErrorSchema.safeParse(value);
  if (!parsed.success || orderErrors[parsed.data.error.code].status !== status)
    return orderError("temporarily_unavailable", allow);
  return orderError(parsed.data.error.code, allow, parsed.data.request_id);
}
export async function staffOrdersRequest(
  request: Request,
  target: OrderReadTarget,
  config: OrderConfig,
): Promise<Response> {
  if (request.method !== "GET") return orderError("method_not_allowed");
  try {
    if (!originAllowed(request.headers, config.origin, false))
      return orderError("forbidden");
    const length = request.headers.get("content-length");
    if (
      request.body !== null ||
      request.headers.has("transfer-encoding") ||
      request.headers.has("trailer") ||
      (length !== null && length !== "0")
    )
      return orderError("invalid_input");
    if (target.kind !== "list" && !orderIdSchema.safeParse(target.id).success)
      return orderError("invalid_input");
    if (
      target.kind === "units" &&
      !orderIdSchema.safeParse(target.summaryId).success
    )
      return orderError("invalid_input");
    const url = new URL(request.url);
    let query: URLSearchParams;
    try {
      const raw = url.search || (request.url.endsWith("?") ? "?" : "");
      query = ordersQuery(raw);
      if (target.kind === "detail" && (query.size || raw))
        return orderError("invalid_input");
    } catch {
      return orderError("invalid_input");
    }
    const access = await orderAccess(request.headers, config);
    if (access instanceof Response) return access;
    const path =
      target.kind === "list"
        ? "/v1/staff/orders"
        : target.kind === "detail"
          ? `/v1/staff/orders/${target.id}`
          : `/v1/staff/orders/${target.id}/summaries/${target.summaryId}/units`;
    const response = await orderGo(config, {
      method: "GET",
      token: access.token,
      path: path + (query.size ? `?${query}` : ""),
    });
    if (response.status !== 200)
      return orderGoError(response.status, response.value);
    if (target.kind === "list") {
      const parsed = orderListSchema.parse(response.value);
      if (parsed.data.items.length > Number(query.get("limit") ?? 50))
        throw new Error("Page bound");
    } else if (target.kind === "detail") {
      if (orderDetailSchema.parse(response.value).data.id !== target.id)
        throw new Error("Order identity");
    } else {
      const parsed = orderUnitsSchema.parse(response.value);
      if (
        parsed.data.summary_id !== target.summaryId ||
        parsed.data.items.length > Number(query.get("limit") ?? 50) ||
        parsed.data.items.length > parsed.data.unit_count
      )
        throw new Error("Summary identity/page bound");
    }
    return new Response(new Uint8Array(response.raw), {
      headers: {
        "Content-Type": "application/json",
        "Cache-Control": "no-store",
      },
    });
  } catch {
    return orderError("temporarily_unavailable");
  }
}
