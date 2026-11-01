import { defineConfig } from "vitest/config";
import base from "./vitest.config.mts";

// Opt-in Go-owned fixture channel; ordinary offline discovery stays unchanged.
export default defineConfig({
  ...base,
  envDir: false,
  test: {
    ...base.test,
    include: ["tests/integration/staff-orders-bff.connected.mts"],
  },
});
