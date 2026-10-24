import {
  type AvailabilityKind,
  type AvailabilityTarget,
  availabilityAcknowledgment,
  availabilityBody,
  availabilityErrorSchema,
  availabilityErrors,
} from "../../../contracts/staff-availability.ts";

type Intent = Readonly<{ key: string; body: string }>;
export type IntentState =
  | { kind: "idle" }
  | { kind: "pending" | "uncertain"; intent: Intent }
  | { kind: "conflict" | "success" | "denied" | "rejected" };
export function beginIntent(
  kind: AvailabilityKind,
  version: string,
  desired: boolean,
  key: string,
): Extract<IntentState, { intent: Intent }> {
  return {
    kind: "pending",
    intent: Object.freeze({
      key,
      body: JSON.stringify({
        expected_version: version,
        [kind === "item" ? "available" : "open"]: desired,
      }),
    }),
  };
}
export function retryIntent(
  state: IntentState,
): Extract<IntentState, { intent: Intent }> {
  if (state.kind !== "uncertain")
    throw new Error("Only an uncertain intent can be retried");
  return { kind: "pending", intent: state.intent };
}
export function intentOutcome(
  status: number,
  value: unknown,
  target: AvailabilityTarget,
  pending: ReturnType<typeof beginIntent>,
): Parameters<typeof settleIntent>[1] {
  if (status === 200) {
    try {
      availabilityAcknowledgment(
        value,
        target,
        availabilityBody(pending.intent.body, target.kind),
      );
      return "success";
    } catch {
      return "uncertain";
    }
  }
  const error = availabilityErrorSchema.safeParse(value);
  if (
    !error.success ||
    availabilityErrors[error.data.error.code].status !== status
  )
    return "uncertain";
  if (status === 401 || status === 403) return "denied";
  if (status === 409) return "conflict";
  if ([400, 404, 413, 415].includes(status)) return "rejected";
  return "uncertain";
}
export function settleIntent(
  state: Extract<IntentState, { intent: Intent }>,
  outcome: "uncertain" | "conflict" | "success" | "denied" | "rejected",
): IntentState {
  return outcome === "uncertain"
    ? { kind: outcome, intent: state.intent }
    : { kind: outcome };
}
