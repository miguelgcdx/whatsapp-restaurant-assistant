import { runtimeAuth } from "@/server/auth/runtime";
import { catalogError, staffCatalogRequest } from "@/server/staff-catalog";

async function handle(request: Request) {
  if (request.method !== "GET") return catalogError(405);
  try {
    return await staffCatalogRequest(request, await runtimeAuth());
  } catch {
    return catalogError(503);
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
