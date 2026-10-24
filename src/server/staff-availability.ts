import "server-only";
import { randomBytes } from "node:crypto";
import { request as httpRequest } from "node:http";
import {
  type AvailabilityTarget,
  availabilityAcknowledgment,
  availabilityBody,
  availabilityErrorSchema,
  availabilityErrors,
  itemSchema,
} from "../contracts/staff-availability.ts";
import { localOrigin, originAllowed } from "./auth/config.ts";
import type { StaffAuth } from "./auth/factory.ts";
import { authoritativeSession } from "./auth/session.ts";

type Code = keyof typeof availabilityErrors;
type Config = { auth: StaffAuth; origin: string; apiOrigin: string };
export function availabilityError(code: Code) {
  const { status, message } = availabilityErrors[code];
  return Response.json(
    { error: { code, message }, request_id: randomBytes(16).toString("hex") },
    {
      status,
      headers: {
        "Cache-Control": "no-store",
        ...(status === 405 ? { Allow: "PATCH" } : {}),
      },
    },
  );
}
function writeGo(
  apiOrigin: string,
  path: string,
  token: string,
  key: string,
  body: Buffer,
): Promise<{ status: number; raw: Buffer; value: unknown }> {
  const origin = localOrigin(apiOrigin);
  return new Promise((resolve, reject) => {
    const request = httpRequest(
      `${origin}${path}`,
      {
        method: "PATCH",
        agent: false,
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
          "Content-Length": body.length,
          Authorization: `Bearer ${token}`,
          "Idempotency-Key": key,
        },
      },
      (response) => {
        const chunks: Buffer[] = [];
        let size = 0;
        response.on("data", (chunk: Buffer) => {
          size += chunk.length;
          if (size > 512 * 1024) {
            request.destroy(new Error("Bounded response"));
            response.destroy();
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
            const text = new TextDecoder("utf-8", {
              fatal: true,
              ignoreBOM: true,
            }).decode(raw);
            const value: unknown = JSON.parse(text);
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
    request.end(body);
  });
}
async function rawBody(request: Request): Promise<Buffer | null> {
  const reader = request.body?.getReader();
  if (!reader) return Buffer.alloc(0);
  const chunks: Buffer[] = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) return Buffer.concat(chunks);
      size += value.byteLength;
      if (size > 64 * 1024) {
        await reader.cancel();
        return null;
      }
      chunks.push(Buffer.from(value));
    }
  } finally {
    reader.releaseLock();
  }
}
export async function staffAvailabilityRequest(
  request: Request,
  target: AvailabilityTarget,
  config: Config,
): Promise<Response> {
  if (request.method !== "PATCH")
    return availabilityError("method_not_allowed");
  try {
    localOrigin(config.origin);
    if (!originAllowed(request.headers, config.origin, true))
      return availabilityError("forbidden");
    const url = new URL(request.url);
    if (url.search || request.url.includes("?"))
      return availabilityError("invalid_input");
    if (
      target.kind === "item" &&
      !itemSchema.shape.id.safeParse(target.id).success
    )
      return availabilityError("invalid_input");
    if (
      request.headers.has("trailer") ||
      request.headers.has("transfer-encoding")
    )
      return availabilityError("invalid_input");
    const key = request.headers.get("idempotency-key");
    if (!key || !/^[\x21-\x7e]{1,128}$/.test(key))
      return availabilityError("invalid_input");
    const media = request.headers.get("content-type") ?? "";
    if (
      !/^application\/json(?:\s*;\s*charset\s*=\s*(?:utf-8|"utf-8"))?$/i.test(
        media,
      )
    )
      return availabilityError("unsupported_media_type");
    const raw = await rawBody(request);
    if (raw === null) return availabilityError("payload_too_large");
    const length = request.headers.get("content-length");
    if (
      length !== null &&
      (!/^(0|[1-9][0-9]*)$/.test(length) ||
        BigInt(length) !== BigInt(raw.length))
    )
      return availabilityError("invalid_input");
    let intent: ReturnType<typeof availabilityBody>;
    try {
      intent = availabilityBody(
        new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(raw),
        target.kind,
      );
    } catch {
      return availabilityError("invalid_input");
    }
    const session = await authoritativeSession(config.auth, request.headers);
    if (!session) return availabilityError("unauthenticated");
    const { token } = await config.auth.api.getToken({
      headers: request.headers,
    });
    const path =
      target.kind === "item"
        ? `/v1/staff/catalog/items/${target.id}/availability`
        : "/v1/staff/restaurant/availability";
    const response = await writeGo(config.apiOrigin, path, token, key, raw);
    if (response.status !== 200) {
      const error = availabilityErrorSchema.safeParse(response.value);
      if (
        !error.success ||
        availabilityErrors[error.data.error.code].status !== response.status
      )
        return availabilityError("temporarily_unavailable");
      return availabilityError(error.data.error.code);
    }
    availabilityAcknowledgment(response.value, target, intent);
    // Do not reserialize: replay includes the original Go request ID and bytes.
    return new Response(new Uint8Array(response.raw), {
      headers: {
        "Content-Type": "application/json",
        "Cache-Control": "no-store",
      },
    });
  } catch {
    return availabilityError("temporarily_unavailable");
  }
}
