import { z } from "zod";

// The browser receives access context, never an API token or caller-owned role.
export const staffAccessSchema = z.strictObject({
  restaurant_id: z.uuid().brand<"RestaurantID">(),
});

export type StaffAccess = z.infer<typeof staffAccessSchema>;
