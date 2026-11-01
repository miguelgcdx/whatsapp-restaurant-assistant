import { defineConfig } from "vitest/config";
import base from "./vitest.config.mts";

// Offline pickup checks never load local credentials or provision auth databases.
export default defineConfig({
  ...base,
  envDir: false,
  test: {
    ...base.test,
    include: [
      "tests/contracts/staff-orders*.test.ts",
      "tests/contracts/workspace-navigation.test.ts",
      "tests/auth/staff-orders*.test.ts",
    ],
  },
});
