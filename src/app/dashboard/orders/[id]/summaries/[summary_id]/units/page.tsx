import { headers } from "next/headers";
import { loadOrders, type OrderSearch } from "../../../../order-load";
import { OrderReadError, OrderUnitsView } from "../../../../order-views";

export default async function UnitsPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string; summary_id: string }>;
  searchParams: Promise<OrderSearch>;
}) {
  const requestHeaders = await headers();
  const { id, summary_id } = await params;
  const result = await loadOrders(
    requestHeaders,
    { kind: "units", id, summaryId: summary_id },
    await searchParams,
  );
  return result.kind === "units" ? (
    <OrderUnitsView id={id} data={result.data} limit={result.limit} />
  ) : result.kind === "error" ? (
    <OrderReadError {...result} />
  ) : (
    <OrderReadError
      status={503}
      message="Orders are temporarily unavailable. Try again later."
    />
  );
}
