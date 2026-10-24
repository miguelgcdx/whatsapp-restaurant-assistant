import "server-only";
import { randomBytes } from "node:crypto";
import { request as httpRequest } from "node:http";
import {
  type Catalog,
  catalogQuery,
  catalogSchema,
} from "../contracts/staff-catalog.ts";
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
    message: "Catalog is temporarily unavailable.",
  },
} as const;
type Failure = keyof typeof errors;
type Config = { auth: StaffAuth; origin: string; apiOrigin: string };
export type CatalogResult =
  | { kind: "allowed"; catalog: Catalog }
  | { kind: "denied"; status: Failure };
export function catalogError(status: Failure) {
  return Response.json(
    { error: errors[status], request_id: randomBytes(16).toString("hex") },
    {
      status,
      headers: {
        "Cache-Control": "no-store",
        ...(status === 405 ? { Allow: "GET" } : {}),
      },
    },
  );
}
function readGo(
  apiOrigin: string,
  token: string,
  query: URLSearchParams,
): Promise<{ status: number; body: unknown }> {
  const origin = localOrigin(apiOrigin);
  const suffix = query.size ? `?${query}` : "";
  return new Promise((resolve, reject) => {
    const request = httpRequest(
      `${origin}/v1/staff/catalog${suffix}`,
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
            // Error payloads are never exposed or interpreted as application data.
            const status = response.statusCode ?? 503;
            const body: unknown =
              status === 200
                ? JSON.parse(
                    new TextDecoder("utf-8", {
                      fatal: true,
                      ignoreBOM: true,
                    }).decode(Buffer.concat(chunks)),
                  )
                : null;
            resolve({ status, body });
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
    request.end();
  });
}
export async function staffCatalog(
  headers: Headers,
  rawQuery: string,
  config: Config,
): Promise<CatalogResult> {
  if (!originAllowed(headers, config.origin, false))
    return { kind: "denied", status: 403 };
  let query: URLSearchParams;
  try {
    query = catalogQuery(rawQuery);
  } catch {
    return { kind: "denied", status: 400 };
  }
  try {
    const session = await authoritativeSession(config.auth, headers);
    if (!session) return { kind: "denied", status: 401 };
    const { token } = await config.auth.api.getToken({ headers });
    const response = await readGo(config.apiOrigin, token, query);
    if (response.status === 403 || response.status === 400)
      return { kind: "denied", status: response.status };
    if (response.status !== 200) return { kind: "denied", status: 503 };
    const catalog = catalogSchema.parse(response.body);
    if (catalog.data.items.length > Number(query.get("limit") ?? 50))
      return { kind: "denied", status: 503 };
    return { kind: "allowed", catalog };
  } catch {
    return { kind: "denied", status: 503 };
  }
}
export async function staffCatalogRequest(request: Request, config: Config) {
  if (!originAllowed(request.headers, config.origin, false))
    return catalogError(403);
  if (request.method !== "GET") return catalogError(405);
  const length = request.headers.get("content-length");
  if (
    request.body !== null ||
    request.headers.has("transfer-encoding") ||
    request.headers.has("trailer") ||
    (length !== null && length !== "0")
  )
    return catalogError(400);
  const url = new URL(request.url);
  const result = await staffCatalog(
    request.headers,
    url.search || (request.url.endsWith("?") ? "?" : ""),
    config,
  );
  return result.kind === "allowed"
    ? Response.json(result.catalog, {
        headers: { "Cache-Control": "no-store" },
      })
    : catalogError(result.status);
}
