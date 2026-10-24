import { runtimeAuth } from "@/server/auth/runtime";
import { accessError, staffAccessRequest } from "@/server/staff-access";

async function handle(request: Request) {
  try {
    return await staffAccessRequest(request, await runtimeAuth());
  } catch {
    return accessError(503);
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
