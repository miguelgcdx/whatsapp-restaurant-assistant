import { headers } from "next/headers";
import Link from "next/link";
import { redirect } from "next/navigation";
import { formatPrice } from "@/contracts/staff-catalog";
import { runtimeAuth } from "@/server/auth/runtime";
import { staffCatalog } from "@/server/staff-catalog";
import { StaffFrame } from "../staff-frame";
import { AvailabilityControl } from "./availability-controls";

export default async function CatalogPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const requestHeaders = await headers();
  const config = await runtimeAuth().catch(() => null);
  if (!config) redirect("/unavailable");
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(await searchParams)) {
    for (const entry of Array.isArray(value)
      ? value
      : value === undefined
        ? []
        : [value])
      query.append(key, entry);
  }
  const result = await staffCatalog(requestHeaders, query.toString(), config);
  if (result.kind === "denied") {
    if (result.status === 401) redirect("/sign-in");
    if (result.status === 403) redirect("/access-denied");
    return (
      <StaffFrame section="catalog">
        <h1>Staff catalog</h1>
        <p role="alert">
          {result.status === 400
            ? "This catalog page is not valid. Start again."
            : "Catalog is temporarily unavailable. Try again later."}
        </p>
        <Link href="/dashboard/catalog" prefetch={false}>
          Start again
        </Link>
        <p>
          <Link href="/dashboard" prefetch={false}>
            Back to dashboard
          </Link>
        </p>
      </StaffFrame>
    );
  }
  const { items, restaurant, next_cursor } = result.catalog.data;
  const next = new URLSearchParams();
  if (query.has("limit")) next.set("limit", query.get("limit") ?? "50");
  if (next_cursor !== null) next.set("cursor", next_cursor);
  return (
    <StaffFrame section="catalog">
      <header className="page-header">
        <p className="eyebrow">Catalog</p>
        <h1>Staff catalog</h1>
        <p>Review the current menu and manage availability.</p>
      </header>
      <div className="panel restaurant-panel">
        <div>
          <h2>Restaurant availability</h2>
          <p>Restaurant is {restaurant.open ? "open" : "closed"}.</p>
        </div>
        <div className="catalog-controls">
          <AvailabilityControl
            target={{ kind: "restaurant" }}
            version={restaurant.version}
            value={restaurant.open}
          />
        </div>
      </div>
      <h2>Current page</h2>
      <p>
        Showing {items.length} items on this page. Availability may change
        between pages.
      </p>
      {items.length === 0 ? (
        <p>The menu is empty.</p>
      ) : (
        <ul className="catalog-items">
          {items.map((item) => (
            <li key={item.id}>
              <h3 id={`catalog-item-${item.id}`}>{item.name}</h3>
              <p className="price">{formatPrice(item.price)}</p>
              <p className="availability-state">
                {item.available ? "Available" : "Unavailable"}
              </p>
              <div className="catalog-controls">
                <AvailabilityControl
                  target={{ kind: "item", id: item.id }}
                  labelledBy={`catalog-item-${item.id}`}
                  version={item.version}
                  value={item.available}
                />
              </div>
            </li>
          ))}
        </ul>
      )}
      <nav aria-label="Catalog pagination">
        {next_cursor !== null ? (
          <Link href={`/dashboard/catalog?${next}`} prefetch={false}>
            Next page
          </Link>
        ) : (
          <p>End of menu.</p>
        )}
        <p>
          <Link href="/dashboard/catalog" prefetch={false}>
            Start again
          </Link>
        </p>
      </nav>
      <Link href="/dashboard" prefetch={false}>
        Back to dashboard
      </Link>
    </StaffFrame>
  );
}
