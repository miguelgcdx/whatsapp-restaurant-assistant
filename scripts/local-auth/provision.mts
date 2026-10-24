import "server-only";
import { z } from "zod";
import type { StaffAuth } from "../../src/server/auth/factory.ts";
import type { SetupState } from "../../src/server/auth/state.ts";

const identity = z.object({
  user: z.object({ id: z.string().min(1).max(256) }),
});
const duplicate = z.object({
  code: z.literal("USER_ALREADY_EXISTS_USE_ANOTHER_EMAIL"),
});
const setupError = () => new Error("Local staff setup is incomplete.");

export async function provisionFixtures(
  auth: StaffAuth,
  state: SetupState,
  origin: string,
  ensure: (subject: string) => Promise<boolean>,
) {
  try {
    for (const fixture of state.fixtures) {
      let response = await auth.api.signUpEmail({
        headers: new Headers({ origin }),
        body: fixture,
        asResponse: true,
      });
      if (
        response.status === 422 &&
        duplicate.safeParse(await response.clone().json()).success
      ) {
        response = await auth.api.signInEmail({
          headers: new Headers({ origin }),
          body: { email: fixture.email, password: fixture.password },
          asResponse: true,
        });
      }
      if (!response.ok) throw setupError();
      const cookie = response.headers
        .getSetCookie()
        .map((value) => value.split(";")[0])
        .join("; ");
      if (!cookie) throw setupError();
      let subject: string;
      try {
        subject = identity.parse(await response.json()).user.id;
      } finally {
        // No setup session/cookie is persisted, including failed cross-owner work.
        await auth.api.signOut({ headers: new Headers({ cookie, origin }) });
      }
      if (!(await ensure(subject))) throw setupError();
    }
    return { complete: true as const, restaurantID: state.restaurantID };
  } catch {
    throw setupError();
  }
}
