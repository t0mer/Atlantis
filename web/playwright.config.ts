import { defineConfig, devices } from "@playwright/test";

// Runs against a live `atlantis serve` backed by atlantis-fake
// (scripts/e2e.sh starts both). Set ATLANTIS_URL to point elsewhere.
export default defineConfig({
  testDir: "e2e",
  timeout: 30_000,
  workers: 1, // one shared server and account
  use: { baseURL: process.env.ATLANTIS_URL ?? "http://127.0.0.1:18081", trace: "retain-on-failure" },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
