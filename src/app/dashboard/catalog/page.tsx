import { headers } from "next/headers";
import { redirect } from "next/navigation";
import { formatPrice } from "@/contracts/staff-catalog";
import { runtimeAuth } from "@/server/auth/runtime";
import { staffCatalog } from "@/server/staff-catalog";
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
      <main>
        <h1>Staff catalog</h1>
        <p role="alert">
          {result.status === 400
            ? "This catalog page is not valid. Start again."
            : "Catalog is temporarily unavailable. Try again later."}
        </p>
        <a href="/dashboard/catalog">Start again</a>
        <p>
          <a href="/dashboard">Back to dashboard</a>
        </p>
      </main>
    );
  }
  const { items, restaurant, next_cursor } = result.catalog.data;
  const next = new URLSearchParams();
  if (query.has("limit")) next.set("limit", query.get("limit") ?? "50");
  if (next_cursor !== null) next.set("cursor", next_cursor);
  return (
    <main>
      <h1>Staff catalog</h1>
      <p>Restaurant is {restaurant.open ? "open" : "closed"}.</p>
      <AvailabilityControl
        target={{ kind: "restaurant" }}
        version={restaurant.version}
        value={restaurant.open}
      />
      <h2>Current page</h2>
      <p>
        Showing {items.length} items on this page. Availability may change
        between pages.
      </p>
      {items.length === 0 ? (
        <p>The menu is empty.</p>
      ) : (
        <ul>
          {items.map((item) => (
            <li key={item.id}>
              <h3>{item.name}</h3>
              <p>{formatPrice(item.price)}</p>
              <p>{item.available ? "Available" : "Unavailable"}</p>
              <AvailabilityControl
                target={{ kind: "item", id: item.id }}
                version={item.version}
                value={item.available}
              />
            </li>
          ))}
        </ul>
      )}
      <nav aria-label="Catalog pagination">
        {next_cursor !== null ? (
          <a href={`/dashboard/catalog?${next}`}>Next page</a>
        ) : (
          <p>End of menu.</p>
        )}
        <p>
          <a href="/dashboard/catalog">Start again</a>
        </p>
      </nav>
      <a href="/dashboard">Back to dashboard</a>
    </main>
  );
}
