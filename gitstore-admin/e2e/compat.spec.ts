import { test, expect } from '@playwright/test'
test('shell navigation and mobile drawer work across engines', async ({
  page,
}) => {
  await page.goto('/w/atelier-kyoto/catalog/products')
  await expect(
    page.getByRole('heading', { name: 'Products', level: 1 }),
  ).toBeVisible()
  const nav = page.getByRole('navigation', {
    name: 'Main navigation',
    exact: true,
  })
  await nav.getByRole('link', { name: 'Products', exact: true }).focus()
  await page.keyboard.press('ArrowDown')
  await expect(
    nav.getByRole('link', { name: 'Variants', exact: true }),
  ).toBeFocused()
  await expect(page).toHaveURL(/catalog\/products$/)
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/catalog\/variants$/)
  await page
    .getByRole('button', { name: 'Search navigation', exact: true })
    .click()
  await page
    .getByRole('searchbox', { name: 'Search pages' })
    .fill('control plane namespaces')
  await page.getByRole('option').click()
  await expect(page).toHaveURL(/\/platform\/namespaces$/)
  await page.reload()
  await expect(
    page.getByRole('heading', { name: 'Namespaces', level: 1 }),
  ).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByRole('button', { name: 'Open navigation' }).click()
  await expect(
    page.getByRole('dialog', { name: 'Mobile navigation' }),
  ).toBeVisible()
  const profile = page.getByRole('button', { name: /Elena Rostova/ })
  await profile.click()
  const menu = page.getByRole('menu').filter({
    has: page.getByRole('menuitem', {
      name: 'Profile & preferences',
      exact: true,
    }),
  })
  await expect(menu).toBeVisible()
  const bounds = (await menu.boundingBox())!
  expect(bounds.x).toBeGreaterThanOrEqual(0)
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(390)
  await page.keyboard.press('Escape')
  await expect(profile).toBeFocused()
  await page
    .getByRole('button', { name: 'Close navigation', exact: true })
    .click()
  await expect(
    page.getByRole('dialog', { name: 'Mobile navigation' }),
  ).toBeHidden()
})
