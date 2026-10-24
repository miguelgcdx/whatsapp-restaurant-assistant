import "server-only";
import { request as httpRequest } from "node:http";
import {
  type StaffAccess,
  staffAccessSchema,
} from "../contracts/staff-access.ts";
import { localOrigin, originAllowed } from "./auth/config.ts";
import type { StaffAuth } from "./auth/factory.ts";
import { authoritativeSession } from "./auth/session.ts";

const errors = {
  400: { code: "invalid_input", message: "Invalid request." },
  401: { code: "unauthenticated", message: "Authentication is required." },
  403: { code: "forbidden", message: "Staff access is not available." },
  405: { code: "method_not_allowed", message: "The method is not allowed." },
  503: {
    code: "temporarily_unavailable",
    message: "Staff access is temporarily unavailable.",
  },
} as const;
export type AccessFailure = keyof typeof errors;
export type AccessResult =
  | { kind: "allowed"; access: StaffAccess; name: string }
  | { kind: "denied"; status: AccessFailure };

function readGo(
  apiOrigin: string,
  token: string,
): Promise<{ status: number; body: string }> {
  const origin = localOrigin(apiOrigin);
  return new Promise((resolve, reject) => {
    const request = httpRequest(
      `${origin}/v1/staff/access`,
      {
        method: "GET",
        agent: false,
        headers: {
          Accept: "application/json",
          Authorization: `Bearer ${token}`,
        },
      },
      (response) => {
        const chunks: Buffer[] = [];
        let size = 0;
        response.on("data", (chunk: Buffer) => {
          size += chunk.length;
          if (size > 16384) {
            response.destroy();
            request.destroy(new Error("bounded response"));
            return;
          }
          chunks.push(chunk);
        });
        response.on("error", reject);
        response.on("end", () =>
          resolve({
            status: response.statusCode ?? 503,
            body: Buffer.concat(chunks).toString("utf8"),
          }),
        );
      },
    );
    // Absolute deadline: trickled bytes cannot extend an idle timeout indefinitely.
    const deadline = setTimeout(
      () => request.destroy(new Error("dependency deadline")),
      2000,
    );
    request.once("close", () => clearTimeout(deadline));
    request.once("error", reject);
    request.end();
  });
}

export async function staffAccess(
  headers: Headers,
  config: { auth: StaffAuth; origin: string; apiOrigin: string },
): Promise<AccessResult> {
  if (!originAllowed(headers, config.origin, false))
    return { kind: "denied", status: 403 };
  try {
    const session = await authoritativeSession(config.auth, headers);
    if (!session) return { kind: "denied", status: 401 };
    const { token } = await config.auth.api.getToken({ headers });
    const response = await readGo(config.apiOrigin, token);
    if (response.status === 403) return { kind: "denied", status: 403 };
    if (response.status !== 200) return { kind: "denied", status: 503 };
    const access = staffAccessSchema.parse(JSON.parse(response.body));
    return { kind: "allowed", access, name: session.user.name };
  } catch {
    return { kind: "denied", status: 503 };
  }
}

export function accessError(status: AccessFailure) {
  return Response.json(
    { error: errors[status] },
    {
      status,
      headers: {
        "Cache-Control": "no-store",
        ...(status === 405 ? { Allow: "GET" } : {}),
      },
    },
  );
}
export async function staffAccessRequest(
  request: Request,
  config: { auth: StaffAuth; origin: string; apiOrigin: string },
) {
  if (!originAllowed(request.headers, config.origin, request.method !== "GET"))
    return accessError(403);
  if (request.method !== "GET") return accessError(405);
  // Next may discard a GET stream; retain the framing metadata check and never
  // consume an unbounded request body just to decide whether it is permitted.
  const contentLength = request.headers.get("content-length");
  if (
    request.body !== null ||
    request.headers.has("transfer-encoding") ||
    (contentLength !== null && contentLength !== "0")
  )
    return accessError(400);
  if (new URL(request.url).search) return accessError(400);
  const result = await staffAccess(request.headers, config);
  return result.kind === "allowed"
    ? Response.json(result.access, { headers: { "Cache-Control": "no-store" } })
    : accessError(result.status);
}
