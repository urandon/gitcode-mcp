import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  // Keep the stateful operator-flow suite serial within its file. Each test owns
  // API routes and exercises multi-request control lifecycles; parallel files
  // still run concurrently without stampeding Vite's first-page transform.
  fullyParallel: false,
  retries: process.env.CI ? 2 : 0,
  reporter: 'line',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'on-first-retry'
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'npm run dev -- --host 127.0.0.1 --port 4173',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI
  }
});
