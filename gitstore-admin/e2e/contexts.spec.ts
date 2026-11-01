import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

test('Settings replaces workspace navigation, preserves namespace, and returns through history', async ({
  page,
}, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto('/w/studio-berlin/settings/store-details')
  const nav = page.getByRole('navigation', {
    name: 'Main navigation',
    exact: true,
  })
  await expect(nav.getByRole('link')).toHaveCount(11)
  await expect(
    nav.getByRole('link', { name: 'Team & access', exact: true }),
  ).toBeVisible()
  await expect(
    page
      .getByRole('complementary', { name: 'Workspace sidebar' })
      .getByRole('link', { name: 'Settings', exact: true }),
  ).toHaveCount(0)
  await expect(
    nav.getByRole('button', { name: 'Catalog', exact: true }),
  ).toHaveCount(0)
  await page.getByRole('link', { name: 'Team & access', exact: true }).click()
  await page.reload()
  await expect(
    nav.getByRole('link', { name: 'Team & access', exact: true }),
  ).toHaveAttribute('aria-current', 'page')
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.screenshot({
    path: testInfo.outputPath('settings-context.png'),
    fullPage: true,
  })
  await nav.getByRole('link', { name: 'Back to workspace' }).click()
  await expect(page).toHaveURL(/studio-berlin\/home\/overview$/)
  await expect(
    nav.getByRole('link', { name: 'Commerce', exact: true }),
  ).toBeVisible()
  await page.goBack()
  await expect(
    nav.getByRole('link', { name: 'Team & access', exact: true }),
  ).toHaveAttribute('aria-current', 'page')
})

test('area switching exposes only related domains and platform selection leaves the namespace scope', async ({
  page,
}) => {
  await page.goto('/w/studio-berlin/home/overview')
  const nav = page.getByRole('navigation', {
    name: 'Main navigation',
    exact: true,
  })
  for (const name of ['Home', 'Commerce', 'Content', 'Operations', 'Insights'])
    await expect(nav.getByRole('link', { name, exact: true })).toBeVisible()
  await nav.getByRole('link', { name: 'Commerce', exact: true }).click()
  await expect(
    nav.getByRole('button', { name: 'Catalog', exact: true }),
  ).toBeVisible()
  await expect(
    nav.getByRole('button', { name: 'Changes & releases', exact: true }),
  ).toHaveCount(0)
  const area = nav.getByRole('button', { name: 'Commerce', exact: true })
  await area.focus()
  await page.keyboard.press('Enter')
  await expect(
    page.getByRole('menu', { name: 'Commerce', exact: true }),
  ).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(area).toBeFocused()
  await area.click()
  await page.getByRole('menuitem', { name: 'Operations', exact: true }).click()
  await expect(page).toHaveURL(
    /studio-berlin\/channels-markets\/sales-channels$/,
  )
  await expect(
    nav.getByRole('button', { name: 'Changes & releases', exact: true }),
  ).toBeVisible()
  await expect(
    nav.getByRole('button', { name: 'Catalog', exact: true }),
  ).toHaveCount(0)
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Control plane/ }).click()
  await expect(page).toHaveURL(/\/platform\/namespaces$/)
  await expect(
    nav.getByRole('link', { name: 'Repositories', exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('link', { name: 'Settings', exact: true }),
  ).toHaveCount(0)
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Studio Berlin/ }).click()
  await expect(page).toHaveURL(/studio-berlin\/home\/overview$/)
})

test('context navigation works in the mobile drawer and tablet rail', async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/w/studio-berlin/home/overview')
  await page.getByRole('button', { name: 'Open navigation' }).click()
  await page
    .getByRole('navigation', { name: 'Mobile navigation', exact: true })
    .getByRole('link', { name: 'Commerce', exact: true })
    .click()
  await expect(
    page.getByRole('dialog', { name: 'Mobile navigation' }),
  ).toBeHidden()
  await page.getByRole('button', { name: 'Open navigation' }).click()
  const drawer = page.getByRole('dialog', { name: 'Mobile navigation' })
  await drawer.getByRole('button', { name: 'Catalog', exact: true }).click()
  await drawer.getByRole('link', { name: 'Products', exact: true }).click()
  await expect(page).toHaveURL(/studio-berlin\/catalog\/products$/)
  await expect(drawer).toBeHidden()
  await page.setViewportSize({ width: 900, height: 900 })
  await page
    .getByRole('button', { name: 'Workspace areas: Commerce', exact: true })
    .click()
  await page.getByRole('menuitem', { name: 'Content', exact: true }).click()
  await page.getByRole('button', { name: 'Content pages', exact: true }).click()
  await page
    .getByRole('menuitem', { name: 'Translations', exact: true })
    .click()
  await expect(page).toHaveURL(/studio-berlin\/content\/translations$/)
})
