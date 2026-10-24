import { runtimeAuth } from "@/server/auth/runtime";
import {
  availabilityError,
  staffAvailabilityRequest,
} from "@/server/staff-availability";

export async function PATCH(
  request: Request,
  context: { params: Promise<{ id: string }> },
) {
  try {
    const { id } = await context.params;
    return await staffAvailabilityRequest(
      request,
      { kind: "item", id },
      await runtimeAuth(),
    );
  } catch {
    return availabilityError("temporarily_unavailable");
  }
}
function unsupported() {
  return availabilityError("method_not_allowed");
}
export {
  unsupported as GET,
  unsupported as POST,
  unsupported as PUT,
  unsupported as DELETE,
  unsupported as HEAD,
  unsupported as OPTIONS,
};
