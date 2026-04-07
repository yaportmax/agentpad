import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  timeout: 60_000,
  use: {
    baseURL: "http://127.0.0.1:8080",
  },
  webServer: {
    command: "cd .. && AGENTPAD_CONFIG=web/playwright.agentpad.toml go run ./cmd/agentpad serve",
    url: "http://127.0.0.1:8080",
    reuseExistingServer: true,
  },
});
