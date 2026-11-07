import type { Page } from '@playwright/test'
export async function openDemoReset(page: Page) {
  await page.getByRole('button', { name: 'Demo data', exact: true }).click()
  await page.getByRole('button', { name: 'Reset demo', exact: true }).click()
}
