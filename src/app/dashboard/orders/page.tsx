import { headers } from "next/headers";
import { loadOrders, type OrderSearch } from "./order-load";
import { OrderListView, OrderReadError } from "./order-views";

export default async function OrdersPage({
  searchParams,
}: {
  searchParams: Promise<OrderSearch>;
}) {
  const requestHeaders = await headers();
  const result = await loadOrders(
    requestHeaders,
    { kind: "list" },
    await searchParams,
  );
  return result.kind === "list" ? (
    <OrderListView data={result.data} limit={result.limit} />
  ) : result.kind === "error" ? (
    <OrderReadError {...result} />
  ) : (
    <OrderReadError
      status={503}
      message="Orders are temporarily unavailable. Try again later."
    />
  );
}
