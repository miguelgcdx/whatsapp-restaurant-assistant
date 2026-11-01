import { runtimeAuth } from "@/server/auth/runtime";
import { orderError, staffOrdersRequest } from "@/server/staff-orders";

async function handle(
  request: Request,
  context: { params: Promise<{ id: string }> },
) {
  if (request.method !== "GET") return orderError("method_not_allowed");
  try {
    const { id } = await context.params;
    return await staffOrdersRequest(
      request,
      { kind: "detail", id },
      await runtimeAuth(),
    );
  } catch {
    return orderError("temporarily_unavailable");
  }
}
export {
  handle as GET,
  handle as POST,
  handle as PATCH,
  handle as PUT,
  handle as DELETE,
  handle as OPTIONS,
  handle as HEAD,
};
