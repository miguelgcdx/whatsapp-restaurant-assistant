import "server-only";
import { toNextJsHandler } from "better-auth/next-js";
import { originAllowed } from "./config.ts";
import type { StaffAuth } from "./factory.ts";

export async function authRequest(
  request: Request,
  auth: StaffAuth,
  origin: string,
) {
  const headers = { "Cache-Control": "no-store" };
  if (!originAllowed(request.headers, origin, request.method !== "GET"))
    return Response.json(
      {
        error: { code: "forbidden", message: "Staff access is not available." },
      },
      { status: 403, headers },
    );
  const url = new URL(request.url);
  if (
    request.method !== "GET" ||
    url.pathname !== "/api/auth/jwks" ||
    url.search
  )
    return Response.json(
      { error: { code: "not_found", message: "Not found." } },
      { status: 404, headers },
    );
  try {
    const response = await toNextJsHandler(auth).GET(
      new Request(`${origin}/api/auth/jwks`),
    );
    if (!response.ok) throw new Error("JWKS unavailable");
    response.headers.set("Cache-Control", "no-store");
    return response;
  } catch {
    return Response.json(
      {
        error: {
          code: "temporarily_unavailable",
          message: "Staff access is temporarily unavailable.",
        },
      },
      { status: 503, headers },
    );
  }
}
