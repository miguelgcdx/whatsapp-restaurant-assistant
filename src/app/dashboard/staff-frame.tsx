import Link from "next/link";
import type { ReactNode } from "react";

/** Presentation only: route owners retain all authentication and data access. */
export function StaffFrame({
  section,
  children,
}: {
  section: "overview" | "catalog" | "orders";
  children: ReactNode;
}) {
  return (
    <>
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>
      <div className="staff-shell">
        <aside className="staff-sidebar">
          <p className="brand">Service desk</p>
          <p className="eyebrow">Local staff workspace</p>
          <nav aria-label="Staff navigation">
            <Link
              prefetch={false}
              href="/dashboard"
              aria-current={section === "overview" ? "page" : undefined}
            >
              Overview
            </Link>
            <Link
              prefetch={false}
              href="/dashboard/catalog"
              aria-current={section === "catalog" ? "page" : undefined}
            >
              Catalog
            </Link>
            <Link
              prefetch={false}
              href="/dashboard/orders"
              aria-current={section === "orders" ? "page" : undefined}
            >
              Orders
            </Link>
          </nav>
          <p className="sidebar-note">
            A focused workspace for staff access, catalog availability and
            pickup orders.
          </p>
        </aside>
        <main id="main-content" tabIndex={-1} className="workspace-main">
          {children}
        </main>
      </div>
    </>
  );
}
