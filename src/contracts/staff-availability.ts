import { z } from "zod";
import { catalogSchema } from "./staff-catalog.ts";

export const itemSchema = catalogSchema.shape.data.shape.items.element;
export const restaurantSchema = catalogSchema.shape.data.shape.restaurant;
export const itemAvailabilitySchema = z.strictObject({
  data: itemSchema,
  request_id: catalogSchema.shape.request_id,
});
export const restaurantAvailabilitySchema = z.strictObject({
  data: restaurantSchema,
  request_id: catalogSchema.shape.request_id,
});
export const itemIntentSchema = z.strictObject({
  expected_version: itemSchema.shape.version,
  available: itemSchema.shape.available,
});
export const restaurantIntentSchema = z.strictObject({
  expected_version: restaurantSchema.shape.version,
  open: restaurantSchema.shape.open,
});
export type AvailabilityKind = "item" | "restaurant";
export type AvailabilityTarget =
  | { kind: "item"; id: string }
  | { kind: "restaurant" };

// This protocol is deliberately a flat, two-field object, not arbitrary JSON.
// Match the entire grammar before JSON.parse can discard duplicate properties.
const stringToken =
  '"(?:[^"\\\\\\u0000-\\u001f]|\\\\(?:["\\\\/bfnrt]|u[0-9a-fA-F]{4}))*"';
const valueToken = `(?:${stringToken}|true|false)`;
const flatPair = new RegExp(
  `^\\s*\\{\\s*(${stringToken})\\s*:\\s*${valueToken}\\s*,\\s*(${stringToken})\\s*:\\s*${valueToken}\\s*\\}\\s*$`,
);
export function availabilityBody(text: string, kind: AvailabilityKind) {
  // JSON.parse below also rejects non-RFC whitespace matched by JavaScript's \\s.
  const match = flatPair.exec(text);
  if (!match) throw new Error("Invalid request");
  if (JSON.parse(match[1]) === JSON.parse(match[2]))
    throw new Error("Duplicate field");
  const value: unknown = JSON.parse(text);
  return kind === "item"
    ? itemIntentSchema.parse(value)
    : restaurantIntentSchema.parse(value);
}
export function availabilityAcknowledgment(
  value: unknown,
  target: AvailabilityTarget,
  intent: ReturnType<typeof availabilityBody>,
) {
  const parsed =
    target.kind === "item"
      ? itemAvailabilitySchema.parse(value).data
      : restaurantAvailabilitySchema.parse(value).data;
  if (BigInt(parsed.version) !== BigInt(intent.expected_version) + BigInt(1))
    throw new Error("Mismatched version");
  if (target.kind === "item") {
    if (
      !("id" in parsed) ||
      parsed.id !== target.id ||
      !("available" in intent) ||
      parsed.available !== intent.available
    )
      throw new Error("Mismatched item");
  } else if (
    !("open" in parsed) ||
    !("open" in intent) ||
    parsed.open !== intent.open
  )
    throw new Error("Mismatched restaurant");
  return parsed;
}
export const availabilityErrors = {
  invalid_input: { status: 400, message: "Invalid request." },
  unauthenticated: { status: 401, message: "Authentication is required." },
  forbidden: { status: 403, message: "Staff access is not available." },
  not_found: { status: 404, message: "Catalog item was not found." },
  method_not_allowed: { status: 405, message: "The method is not allowed." },
  version_conflict: {
    status: 409,
    message: "Refresh availability before trying again.",
  },
  idempotency_conflict: {
    status: 409,
    message: "Idempotency key was already used for a different request.",
  },
  payload_too_large: { status: 413, message: "Request body is too large." },
  unsupported_media_type: {
    status: 415,
    message: "Content-Type must be application/json.",
  },
  internal_error: {
    status: 500,
    message: "Availability could not be completed.",
  },
  temporarily_unavailable: {
    status: 503,
    message: "Availability is temporarily unavailable.",
  },
} as const;
export const availabilityErrorSchema = z.strictObject({
  error: z.strictObject({
    code: z.enum(
      Object.keys(availabilityErrors) as [
        keyof typeof availabilityErrors,
        ...(keyof typeof availabilityErrors)[],
      ],
    ),
    message: z.string(),
  }),
  request_id: catalogSchema.shape.request_id,
});
