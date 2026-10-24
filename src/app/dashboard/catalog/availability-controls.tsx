"use client";

import { useRouter } from "next/navigation";
import { useEffect, useRef, useState, useTransition } from "react";
import type { AvailabilityTarget } from "../../../contracts/staff-availability.ts";
import {
  beginIntent,
  type IntentState,
  intentOutcome,
  retryIntent,
  settleIntent,
} from "./availability-intent.ts";

type Props = { target: AvailabilityTarget; version: string; value: boolean };
export function AvailabilityControl(props: Props) {
  // Defer browser-only hooks to hydration. SSR keeps an inert, accessible control
  // and never needs a router context, UUID, token or serialized session.
  const [ready, setReady] = useState(false);
  useEffect(() => setReady(true), []);
  return ready ? (
    <InteractiveControl {...props} />
  ) : (
    <button type="button" disabled>
      Loading availability control…
    </button>
  );
}
function InteractiveControl({ target, version, value }: Props) {
  const router = useRouter();
  const [state, setState] = useState<IntentState>({ kind: "idle" });
  const [refreshing, startRefresh] = useTransition();
  const inFlight = useRef(false);
  const refresh = () => startRefresh(() => router.refresh());
  const endpoint =
    target.kind === "item"
      ? `/api/staff/catalog/items/${target.id}/availability`
      : "/api/staff/restaurant/availability";
  async function send(pending: ReturnType<typeof beginIntent>) {
    if (inFlight.current) return;
    inFlight.current = true;
    setState(pending);
    try {
      const response = await fetch(endpoint, {
        method: "PATCH",
        credentials: "same-origin",
        cache: "no-store",
        redirect: "error",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": pending.intent.key,
        },
        body: pending.intent.body,
      });
      const json: unknown = await response.json();
      const outcome = intentOutcome(response.status, json, target, pending);
      setState(settleIntent(pending, outcome));
      if (outcome !== "uncertain") refresh();
    } catch {
      setState(settleIntent(pending, "uncertain"));
    } finally {
      inFlight.current = false;
    }
  }
  const pending = state.kind === "pending";
  const label =
    target.kind === "item"
      ? value
        ? "Mark unavailable"
        : "Mark available"
      : value
        ? "Close restaurant"
        : "Open restaurant";
  const feedback = {
    idle: "",
    pending: "Saving availability…",
    uncertain:
      "The result is uncertain. Retry the same request; do not start a new change.",
    conflict:
      "Availability changed or this key was already used. Review the refreshed availability before a new change.",
    success: "Availability saved. Refreshing current availability.",
    denied: "Staff access is no longer available. Refreshing access.",
    rejected:
      "Availability request was rejected. Review the refreshed availability.",
  }[state.kind];
  return (
    <section
      aria-label={
        target.kind === "item"
          ? "Item availability control"
          : "Restaurant availability control"
      }
      aria-busy={pending || refreshing}
    >
      {state.kind !== "denied" && (
        <button
          type="button"
          disabled={
            pending ||
            refreshing ||
            state.kind === "uncertain" ||
            state.kind === "conflict" ||
            state.kind === "rejected"
          }
          onClick={() =>
            void send(
              beginIntent(target.kind, version, !value, crypto.randomUUID()),
            )
          }
        >
          {label}
        </button>
      )}
      {feedback && <output>{feedback}</output>}
      {state.kind === "uncertain" && (
        <button type="button" onClick={() => void send(retryIntent(state))}>
          Retry same request
        </button>
      )}
      {(state.kind === "conflict" || state.kind === "rejected") && (
        <button
          type="button"
          disabled={refreshing}
          onClick={() => setState({ kind: "idle" })}
        >
          Review refreshed availability
        </button>
      )}
    </section>
  );
}
