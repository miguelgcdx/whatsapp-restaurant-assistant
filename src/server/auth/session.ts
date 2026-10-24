import "server-only";
import type { StaffAuth } from "./factory.ts";

// Deliberately not React.cache/unstable_cache: revocation is checked on every operation.
export async function authoritativeSession(auth: StaffAuth, headers: Headers) {
  return auth.api.getSession({ headers, query: { disableCookieCache: true } });
}
