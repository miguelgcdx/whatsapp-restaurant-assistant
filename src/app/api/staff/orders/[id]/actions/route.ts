import { runtimeAuth } from "@/server/auth/runtime";
import { staffOrderActionRequest } from "@/server/staff-order-actions";
import { orderError } from "@/server/staff-orders";

async function handle(
  request: Request,
  context: { params: Promise<{ id: string }> },
) {
  if (request.method !== "POST")
    return orderError("method_not_allowed", "POST");
  try {
    const { id } = await context.params;
    return await staffOrderActionRequest(request, id, await runtimeAuth());
  } catch {
    return orderError("temporarily_unavailable", "POST");
  }
}
export {
  handle as POST,
  handle as GET,
  handle as PUT,
  handle as PATCH,
  handle as DELETE,
  handle as OPTIONS,
  handle as HEAD,
};
