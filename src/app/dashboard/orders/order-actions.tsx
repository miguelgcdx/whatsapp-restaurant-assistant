"use client";
import { useEffect, useRef, useState } from "react";
import type { OrderAction, OrderDetail } from "@/contracts/staff-orders";
import {
  createOrderIntent,
  loadOrderIntent,
  type OrderIntent,
  orderIntentKey,
  resolveOrderAttempt,
  saveOrderIntent,
  sendOrderIntent,
} from "./order-intent";

const labels: Record<OrderAction, string> = {
  accept: "Accept",
  reject: "Reject",
  start_preparing: "Start preparation",
  mark_ready: "Mark ready",
  mark_picked_up: "Mark picked up",
  cancel: "Cancel",
  mark_paid: "Record payment",
};
type Phase =
  | { kind: "checking" | "ready" }
  | { kind: "pending"; intent: OrderIntent }
  | { kind: "uncertain"; intent: OrderIntent; message: string }
  | { kind: "review" | "blocked"; message: string };
export function ActionOptions({ actions }: { actions: OrderAction[] }) {
  return (
    <>
      {actions.map((action) => (
        <option key={action} value={action}>
          {labels[action]}
        </option>
      ))}
    </>
  );
}
export function OrderActions({ order }: { order: OrderDetail }) {
  const [phase, setPhase] = useState<Phase>({ kind: "checking" });
  const [choice, setChoice] = useState("");
  const busy = useRef(false);
  useEffect(() => {
    try {
      const intent = loadOrderIntent(sessionStorage, order.id);
      setPhase(
        intent
          ? {
              kind: "uncertain",
              intent,
              message:
                "A saved request has not been reconciled. Keep its original intent.",
            }
          : { kind: "ready" },
      );
    } catch {
      setPhase({
        kind: "blocked",
        message:
          "Saved request could not be read. Do not submit another intent.",
      });
    }
  }, [order.id]);
  async function perform(intent: OrderIntent, hadUncertainty: boolean) {
    if (busy.current) return;
    busy.current = true;
    try {
      saveOrderIntent(sessionStorage, intent);
      setPhase({ kind: "pending", intent });
      const result = await sendOrderIntent(intent);
      const resolution = resolveOrderAttempt(result, hadUncertainty);
      if (resolution.kind === "review") {
        sessionStorage.removeItem(orderIntentKey(order.id));
        // An older replay acknowledges an intent, never replaces the current view.
        setPhase(resolution);
      } else setPhase({ ...resolution, intent });
    } catch {
      setPhase({
        kind: "blocked",
        message:
          "Request recovery could not be preserved. Do not submit another intent; keep this order open.",
      });
    } finally {
      busy.current = false;
    }
  }
  function submit() {
    if (phase.kind !== "ready" || busy.current) return;
    const action = order.permitted_actions.find((value) => value === choice);
    if (!action) return;
    try {
      void perform(
        createOrderIntent(order, action, crypto.randomUUID()),
        false,
      );
    } catch {
      setPhase({
        kind: "blocked",
        message: "Request could not be prepared. Nothing was sent.",
      });
    }
  }
  return (
    <section className="order-panel" aria-labelledby="order-action-heading">
      <h2 id="order-action-heading">Process order</h2>
      <p>
        Actions shown were offered by the server at version {order.version}. The
        server rechecks current access, state and policy.
      </p>
      {phase.kind === "checking" && (
        <output>Checking for a saved request…</output>
      )}
      {phase.kind === "ready" &&
        (order.permitted_actions.length === 0 ? (
          <p>No actions offered for this view.</p>
        ) : (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              submit();
            }}
          >
            <label htmlFor="pickup-action">Order action</label>
            <select
              id="pickup-action"
              value={choice}
              onChange={(event) => setChoice(event.target.value)}
              required
            >
              <option value="">Choose an action</option>
              <ActionOptions actions={order.permitted_actions} />
            </select>
            <button
              type="submit"
              disabled={
                !order.permitted_actions.some((action) => action === choice)
              }
            >
              Submit action
            </button>
            <p>
              Payment is recorded only by Record payment; fulfillment never
              collects payment automatically.
            </p>
          </form>
        ))}
      {phase.kind === "pending" && (
        <output>
          Submitting one request. Do not repeat any physical payment collection.
        </output>
      )}
      {phase.kind === "uncertain" && (
        <div role="alert">
          <p>{phase.message}</p>
          <p>
            Reading the order cannot prove which request committed. Retry only
            this saved request, without collecting payment again.
          </p>
          <button
            type="button"
            onClick={() => {
              void perform(phase.intent, true);
            }}
          >
            Reconcile same request
          </button>
        </div>
      )}
      {(phase.kind === "review" || phase.kind === "blocked") && (
        <output>{phase.message}</output>
      )}
      {phase.kind === "review" && (
        <a href={`/dashboard/orders/${order.id}`}>
          Reload order before another action
        </a>
      )}
    </section>
  );
}
