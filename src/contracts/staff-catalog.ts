import { z } from "zod";

const decimalPattern = /^(0|[1-9][0-9]{0,18})$/;
const decimal = z
  .string()
  .refine(
    (value) =>
      decimalPattern.test(value) &&
      BigInt(value) <= BigInt("9223372036854775807"),
  );
const cursor = z
  .string()
  .min(1)
  .max(2048)
  .regex(/^[\x21-\x7f]+$/);
export const priceSchema = z.strictObject({
  amount_minor: decimal,
  currency: z.string().regex(/^[A-Z]{3}$/),
  exponent: z.number().int().min(0).max(6),
});
export const catalogSchema = z.strictObject({
  data: z
    .strictObject({
      items: z
        .array(
          z.strictObject({
            id: z
              .string()
              .regex(
                /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
              ),
            name: z
              .string()
              .refine(
                (value) =>
                  value.isWellFormed() &&
                  !value.includes("\0") &&
                  /[^\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]/u.test(
                    value,
                  ) &&
                  [...value].length <= 200,
              ),
            price: priceSchema,
            available: z.boolean(),
            version: decimal,
          }),
        )
        .max(100),
      next_cursor: cursor.nullable(),
      restaurant: z.strictObject({ open: z.boolean(), version: decimal }),
    })
    .refine(
      (value) =>
        (value.next_cursor === null || value.items.length > 0) &&
        value.items.every(
          (item) =>
            item.price.currency === value.items[0]?.price.currency &&
            item.price.exponent === value.items[0]?.price.exponent,
        ),
    ),
  request_id: z.string().regex(/^[a-f0-9]{32}$/),
});
export type Catalog = z.infer<typeof catalogSchema>;
export function formatPrice(price: z.infer<typeof priceSchema>): string {
  const digits = price.amount_minor.padStart(price.exponent + 1, "0");
  return `${price.currency} ${price.exponent === 0 ? digits : `${digits.slice(0, -price.exponent)}.${digits.slice(-price.exponent)}`}`;
}

// Validate encoding before URLSearchParams can replace malformed UTF-8 with U+FFFD.
export function catalogQuery(raw: string): URLSearchParams {
  if (raw === "?") throw new Error("Invalid query");
  const source = raw.startsWith("?") ? raw.slice(1) : raw;
  if (source)
    for (const part of source.split("&")) {
      if (!part || part.includes(";")) throw new Error("Invalid query");
      decodeURIComponent(part.replaceAll("+", " "));
    }
  const input = new URLSearchParams(source);
  const output = new URLSearchParams();
  for (const [key, value] of input) {
    if (output.has(key)) throw new Error("Invalid query");
    if (key === "limit") {
      if (!/^(?:[1-9][0-9]?|100)$/.test(value))
        throw new Error("Invalid query");
    } else if (key === "cursor") cursor.parse(value);
    else throw new Error("Invalid query");
    output.set(key, value);
  }
  return output;
}
