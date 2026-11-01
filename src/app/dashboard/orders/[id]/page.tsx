import { headers } from "next/headers";
import { OrderActions } from "../order-actions";
import { loadOrders, type OrderSearch } from "../order-load";
import { OrderDetailView, OrderReadError } from "../order-views";

export default async function OrderPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<OrderSearch>;
}) {
  const requestHeaders = await headers();
  const { id } = await params;
  const result = await loadOrders(
    requestHeaders,
    { kind: "detail", id },
    await searchParams,
  );
  return result.kind === "detail" ? (
    <OrderDetailView
      order={result.data}
      actions={<OrderActions key={result.data.id} order={result.data} />}
    />
  ) : result.kind === "error" ? (
    <OrderReadError {...result} />
  ) : (
    <OrderReadError
      status={503}
      message="Orders are temporarily unavailable. Try again later."
    />
  );
}
