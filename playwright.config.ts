import { defineConfig } from "@playwright/test";

// The integration harness owns both listeners and the throwaway database.
const origin = process.env.DASHBOARD_E2E_ORIGIN;
if (!origin || !/^http:\/\/127\.0\.0\.1:[1-9]\d{0,4}$/.test(origin)) {
  throw new Error("Run the owned tests/integration/dashboard.mts harness.");
}
export default defineConfig({
  testDir: "./tests/e2e",
  outputDir: "./backend/.cache/playwright-results",
  workers: 1,
  retries: 0,
  timeout: 30000,
  reporter: "line",
  use: {
    baseURL: origin,
    headless: true,
    trace: "off",
    screenshot: "off",
    video: "off",
  },
});
