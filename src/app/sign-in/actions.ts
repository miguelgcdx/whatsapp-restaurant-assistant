"use server";
import { headers } from "next/headers";
import { redirect } from "next/navigation";
import { z } from "zod";
import { originAllowed } from "@/server/auth/config";
import { runtimeAuth } from "@/server/auth/runtime";
import { authoritativeSession } from "@/server/auth/session";

const credentials = z.strictObject({
  email: z.email().max(254),
  password: z.string().min(1).max(128),
});
export async function signIn(form: FormData) {
  const requestHeaders = await headers();
  const config = await runtimeAuth().catch(() => null);
  if (!config) redirect("/unavailable");
  if (!originAllowed(requestHeaders, config.origin, true))
    redirect("/access-denied");
  const input = credentials.safeParse({
    email: form.get("email"),
    password: form.get("password"),
  });
  if (!input.success) redirect("/sign-in?error=1");
  try {
    await config.auth.api.signInEmail({
      headers: requestHeaders,
      body: input.data,
    });
  } catch {
    redirect("/sign-in?error=1");
  }
  redirect("/dashboard");
}
export async function signOut() {
  const requestHeaders = await headers();
  const config = await runtimeAuth().catch(() => null);
  if (!config) redirect("/unavailable");
  if (!originAllowed(requestHeaders, config.origin, true))
    redirect("/access-denied");
  try {
    const session = await authoritativeSession(config.auth, requestHeaders);
    if (session) await config.auth.api.signOut({ headers: requestHeaders });
  } catch {
    redirect("/unavailable");
  }
  redirect("/sign-in");
}
