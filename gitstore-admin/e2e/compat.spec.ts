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
  await expect(menu).toBeFocused()
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

test('My work mobile editor confirms unsaved dismissal across engines', async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto('/w/atelier-kyoto/home/my-work')
  await page
    .getByRole('button', { name: 'View details for Kanso Stone Basin' })
    .click()
  await page
    .getByRole('button', { name: 'Edit demo draft', exact: true })
    .click()
  const name = page.getByRole('textbox', { name: 'Title', exact: true })
  await name.fill('Mobile basin')
  await page.keyboard.press('Escape')
  const confirmation = page.getByRole('dialog', {
    name: 'Discard unsaved edits?',
  })
  await expect(confirmation).toBeVisible()
  await confirmation
    .getByRole('button', { name: 'Cancel', exact: true })
    .click()
  await expect(name).toHaveValue('Mobile basin')
  await page.getByRole('button', { name: 'Save demo draft' }).click()
  await expect(
    page
      .getByRole('dialog', { name: 'Page context', exact: true })
      .getByRole('heading', { name: 'Mobile basin', exact: true }),
  ).toBeFocused()
  await page.getByRole('button', { name: 'Close inspector' }).click()
  await expect(
    page.getByRole('button', { name: 'View details for Mobile basin' }),
  ).toBeFocused()
})
