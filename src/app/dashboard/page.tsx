import { headers } from "next/headers";
import { redirect } from "next/navigation";
import { runtimeAuth } from "@/server/auth/runtime";
import { staffAccess } from "@/server/staff-access";
import { signOut } from "../sign-in/actions";
export default async function Dashboard() {
  // Suspend on request data before reading configuration or initializing auth.
  const requestHeaders = await headers();
  const config = await runtimeAuth().catch(() => null);
  if (!config) redirect("/unavailable");
  const result = await staffAccess(requestHeaders, config);
  if (result.kind === "denied") {
    if (result.status === 401) redirect("/sign-in");
    if (result.status === 403) redirect("/access-denied");
    redirect("/unavailable");
  }
  return (
    <main>
      <h1>Staff dashboard</h1>
      <p>Signed in as {result.name}.</p>
      <output>Staff access confirmed.</output>
      <p>
        <a href="/dashboard/catalog">View catalog</a>
      </p>
      <form action={signOut}>
        <button type="submit">Sign out</button>
      </form>
    </main>
  );
}
