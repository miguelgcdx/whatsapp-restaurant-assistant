import { authRequest } from "@/server/auth/http";
import { runtimeAuth } from "@/server/auth/runtime";

async function handle(request: Request) {
  try {
    const config = await runtimeAuth();
    return authRequest(request, config.auth, config.origin);
  } catch {
    return Response.json(
      {
        error: {
          code: "temporarily_unavailable",
          message: "Staff access is temporarily unavailable.",
        },
      },
      { status: 503, headers: { "Cache-Control": "no-store" } },
    );
  }
}
export {
  handle as GET,
  handle as POST,
  handle as PUT,
  handle as PATCH,
  handle as DELETE,
  handle as OPTIONS,
  handle as HEAD,
};
