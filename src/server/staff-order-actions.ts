import "server-only";
import {
  orderActionBody,
  orderDetailSchema,
  orderIdSchema,
} from "../contracts/staff-orders";
import { originAllowed } from "./auth/config";
import {
  type OrderConfig,
  orderAccess,
  orderError,
  orderGo,
  orderGoError,
} from "./staff-orders";

async function actionBytes(request: Request): Promise<Buffer | null> {
  const reader = request.body?.getReader();
  if (!reader) return Buffer.alloc(0);
  let timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error("Body deadline")), 2000);
  });
  const chunks: Buffer[] = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await Promise.race([reader.read(), deadline]);
      if (done) return Buffer.concat(chunks);
      size += value.byteLength;
      if (size > 64 * 1024) {
        void reader.cancel().catch(() => {});
        return null;
      }
      chunks.push(Buffer.from(value));
    }
  } catch (error) {
    void reader.cancel().catch(() => {});
    throw error;
  } finally {
    clearTimeout(timer);
    reader.releaseLock();
  }
}
export async function staffOrderActionRequest(
  request: Request,
  id: string,
  config: OrderConfig,
): Promise<Response> {
  if (request.method !== "POST")
    return orderError("method_not_allowed", "POST");
  try {
    if (!originAllowed(request.headers, config.origin, true))
      return orderError("forbidden", "POST");
    if (
      !orderIdSchema.safeParse(id).success ||
      request.url.includes("?") ||
      request.headers.has("transfer-encoding") ||
      request.headers.has("trailer")
    )
      return orderError("invalid_input", "POST");
    const key = request.headers.get("idempotency-key");
    if (!key || !/^[\x21-\x7e]{1,128}$/.test(key))
      return orderError("invalid_input", "POST");
    if (
      !/^application\/json(?:\s*;\s*charset\s*=\s*(?:utf-8|"utf-8"))?$/i.test(
        request.headers.get("content-type") ?? "",
      )
    )
      return orderError("unsupported_media_type", "POST");
    const raw = await actionBytes(request);
    if (raw === null) return orderError("payload_too_large", "POST");
    const length = request.headers.get("content-length");
    if (
      length !== null &&
      (!/^(0|[1-9][0-9]*)$/.test(length) ||
        BigInt(length) !== BigInt(raw.length))
    )
      return orderError("invalid_input", "POST");
    try {
      orderActionBody(
        new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(raw),
      );
    } catch {
      return orderError("invalid_input", "POST");
    }
    const access = await orderAccess(request.headers, config, true);
    if (access instanceof Response) return access;
    const response = await orderGo(config, {
      method: "POST",
      path: `/v1/staff/orders/${id}/actions`,
      body: raw,
      key,
      token: access.token,
    });
    if (response.status !== 200)
      return orderGoError(response.status, response.value, "POST");
    const result = orderDetailSchema.parse(response.value);
    if (result.data.id !== id)
      return orderError("temporarily_unavailable", "POST");
    // Original replay may predate live state; never impose expected_version+1.
    return new Response(new Uint8Array(response.raw), {
      headers: {
        "Content-Type": "application/json",
        "Cache-Control": "no-store",
      },
    });
  } catch {
    return orderError("temporarily_unavailable", "POST");
  }
}
