import Link from "next/link";
import type { ReactNode } from "react";
import type { z } from "zod";
import {
  formatOrderMoney,
  type OrderDetail,
  type orderListSchema,
  type orderUnitsSchema,
} from "@/contracts/staff-orders";

type ListData = z.infer<typeof orderListSchema>["data"];
type UnitsData = z.infer<typeof orderUnitsSchema>["data"];
function nextHref(path: string, cursor: string, limit?: string) {
  const query = new URLSearchParams();
  if (limit !== undefined) query.set("limit", limit);
  query.set("cursor", cursor);
  return `${path}?${query}`;
}

export function OrderListView({
  data,
  limit,
}: {
  data: ListData;
  limit?: string;
}) {
  return (
    <div className="orders-view">
      <header>
        <p className="eyebrow">Pickup</p>
        <h1>Staff orders</h1>
        <p>
          Current page: {data.items.length} orders. Status may change between
          pages.
        </p>
      </header>
      {data.items.length === 0 ? (
        <p>No orders on this page.</p>
      ) : (
        <ul className="orders-list">
          {data.items.map((order) => (
            <li key={order.id}>
              <h2>
                <Link href={`/dashboard/orders/${order.id}`} prefetch={false}>
                  Order {order.id}
                </Link>
              </h2>
              <p>
                Created:{" "}
                <time dateTime={order.created_at}>{order.created_at}</time>
              </p>
              <p>Fulfillment: {order.fulfillment_status}</p>
              <p>Payment: {order.payment_status}</p>
              <p>Pickup total: {formatOrderMoney(order.total)}</p>
            </li>
          ))}
        </ul>
      )}
      <nav aria-label="Orders pagination">
        {data.next_cursor !== null ? (
          <Link
            href={nextHref("/dashboard/orders", data.next_cursor, limit)}
            prefetch={false}
          >
            Next page
          </Link>
        ) : (
          <p>End of orders.</p>
        )}
      </nav>
    </div>
  );
}

/** Keep the read projection independent from the mutation's retained intent. */
export function OrderDetailView({
  order,
  actions,
}: {
  order: OrderDetail;
  actions?: ReactNode;
}) {
  return (
    <div className="orders-view">
      <header>
        <p className="eyebrow">Pickup order</p>
        <h1>Order {order.id}</h1>
        <p>Version: {order.version}</p>
        <p>
          Created: <time dateTime={order.created_at}>{order.created_at}</time>
        </p>
      </header>
      <section className="order-panel" aria-label="Order status">
        <h2>Current status</h2>
        <p>Fulfillment: {order.fulfillment_status}</p>
        <p>Payment: {order.payment.state}</p>
        <p>Collection time: {order.payment.collected_at ?? "Not provided"}</p>
        <p>Payment method: Pay at pickup</p>
      </section>
      <section className="order-panel" aria-label="Confirmed summary">
        <h2>Confirmed summary</h2>
        <p>Summary: {order.confirmed.id}</p>
        <p>{order.confirmed.unit_count} units in confirmed summary</p>
        <p>Subtotal: {formatOrderMoney(order.confirmed.subtotal)}</p>
        <p>Delivery fee: {formatOrderMoney(order.confirmed.delivery_fee)}</p>
        <p>Confirmed total: {formatOrderMoney(order.confirmed.total)}</p>
        <p>Current order total: {formatOrderMoney(order.total)}</p>
        <Link
          href={`/dashboard/orders/${order.id}/summaries/${order.confirmed.id}/units`}
          prefetch={false}
        >
          View exact confirmed units
        </Link>
      </section>
      <section className="order-panel" aria-label="Preparation estimate">
        <h2>Preparation</h2>
        <p>
          Preparation estimate:{" "}
          {order.estimate === null
            ? "Not provided"
            : `${order.estimate.minutes} minutes`}
        </p>
        <p>Preparation only — not a ready or pickup promise.</p>
        {order.estimate !== null && (
          <>
            <p>
              As of:{" "}
              <time dateTime={order.estimate.as_of}>
                {order.estimate.as_of}
              </time>
            </p>
            <p>Provisional: {order.estimate.provisional ? "yes" : "no"}</p>
          </>
        )}
        <p>
          Preparation started: {order.preparation_started_at ?? "Not provided"}
        </p>
      </section>
      {actions}
      <Link href="/dashboard/orders" prefetch={false}>
        Back to orders
      </Link>
    </div>
  );
}

export function OrderUnitsView({
  id,
  data,
  limit,
}: {
  id: string;
  data: UnitsData;
  limit?: string;
}) {
  return (
    <div className="orders-view">
      <header>
        <h1>Confirmed pickup units</h1>
        <p>Order: {id}</p>
        <p>Immutable summary: {data.summary_id}</p>
        <p>
          {data.unit_count} units in confirmed summary; {data.items.length} on
          this page.
        </p>
      </header>
      {data.items.length === 0 ? (
        <p>No units on this page.</p>
      ) : (
        <ul className="orders-list">
          {data.items.map((unit) => (
            <li key={unit.id}>
              <h2>{unit.name}</h2>
              <p className="order-note">{unit.note}</p>
              <p>Unit price: {formatOrderMoney(unit.unit_price)}</p>
              <p>Unit: {unit.id}</p>
            </li>
          ))}
        </ul>
      )}
      <nav aria-label="Confirmed units pagination">
        {data.next_cursor !== null ? (
          <Link
            href={nextHref(
              `/dashboard/orders/${id}/summaries/${data.summary_id}/units`,
              data.next_cursor,
              limit,
            )}
            prefetch={false}
          >
            Next page
          </Link>
        ) : (
          <p>End of confirmed units.</p>
        )}
      </nav>
      <Link href={`/dashboard/orders/${id}`} prefetch={false}>
        Back to order
      </Link>
    </div>
  );
}

export function OrderReadError({
  message,
  status,
}: {
  message: string;
  status: number;
}) {
  return (
    <div className="orders-view">
      <h1>Staff orders</h1>
      <p role="alert">
        {status}: {message}
      </p>
      <Link href="/dashboard/orders" prefetch={false}>
        Start again
      </Link>
    </div>
  );
}
