import { headers } from "next/headers";
import Link from "next/link";
import { redirect } from "next/navigation";
import { runtimeAuth } from "@/server/auth/runtime";
import { staffAccess } from "@/server/staff-access";
import { signOut } from "../sign-in/actions";
import { StaffFrame } from "./staff-frame";
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
    <StaffFrame section="overview">
      <header className="page-header">
        <p className="eyebrow">Overview</p>
        <h1>Staff dashboard</h1>
        <p>Signed in as {result.name}.</p>
      </header>
      <output className="notice">Staff access confirmed.</output>
      <div className="overview-grid">
        <section className="panel" aria-labelledby="catalog-next-step">
          <h2 id="catalog-next-step">Manage availability</h2>
          <p>
            Open the catalog to review current restaurant and item availability,
            browse menu pages, and make availability changes.
          </p>
          <Link
            className="action-link"
            href="/dashboard/catalog"
            prefetch={false}
          >
            View catalog
          </Link>
        </section>
        <section className="panel" aria-labelledby="workspace-session">
          <h2 id="workspace-session">Your workspace session</h2>
          <p>
            Access is checked against your current staff membership. Sign out
            when you finish using this workspace.
          </p>
          <form action={signOut}>
            <button type="submit">Sign out</button>
          </form>
        </section>
      </div>
      <section
        className="workspace-note"
        aria-labelledby="availability-guidance"
      >
        <h2 id="availability-guidance">Review before changing</h2>
        <p>
          The catalog shows server-confirmed availability. If a change conflicts
          or its result is uncertain, follow the feedback beside that control
          before making another change.
        </p>
      </section>
    </StaffFrame>
  );
}
