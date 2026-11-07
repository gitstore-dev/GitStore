import { defineConfig, devices } from '@playwright/test'
const storybookPort = process.env.STORYBOOK_PORT ?? '6006'
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  globalTimeout: process.env.CI ? 180_000 : undefined,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: { baseURL: 'http://127.0.0.1:4173', trace: 'retain-on-failure' },
  webServer: [
    {
      command: 'pnpm preview',
      gracefulShutdown: { signal: 'SIGTERM', timeout: 5_000 },
      url: 'http://127.0.0.1:4173',
      reuseExistingServer: !process.env.CI,
    },
    {
      command: `pnpm exec vite preview --host 127.0.0.1 --port ${storybookPort} --strictPort --outDir storybook-static`,
      gracefulShutdown: { signal: 'SIGTERM', timeout: 5_000 },
      url: `http://127.0.0.1:${storybookPort}`,
      reuseExistingServer: !process.env.CI,
    },
  ],
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 1440, height: 1000 },
      },
    },
    {
      name: 'firefox',
      testMatch: '**/compat.spec.ts',
      use: { ...devices['Desktop Firefox'] },
    },
    {
      name: 'webkit',
      testMatch: '**/compat.spec.ts',
      use: { ...devices['Desktop Safari'] },
    },
  ],
})
