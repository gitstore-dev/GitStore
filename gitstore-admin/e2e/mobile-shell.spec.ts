import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

test('context selection stays checked, fits inside the sidebar, and removes redundant labels', async ({
  page,
}, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto('/w/atelier-kyoto/home/overview')
  const sidebar = page.getByRole('complementary', { name: 'Workspace sidebar' })
  await expect(
    sidebar.getByText('DEMO WORKSPACE', { exact: true }),
  ).toHaveCount(0)
  await expect(
    sidebar.getByText('YOUR WORKSPACE', { exact: true }),
  ).toHaveCount(0)
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toHaveText(
    'HomeOverview',
  )
  const picker = page.getByRole('button', { name: /Workspace context/ })
  for (const name of [
    'Atelier Kyoto',
    'Studio Berlin',
    'Control plane',
    'Atelier Kyoto',
  ]) {
    await picker.click()
    await expect(
      page.getByRole('group', { name: 'Stores', exact: true }),
    ).toBeVisible()
    await expect(
      page.getByRole('group', { name: 'Platform', exact: true }),
    ).toBeVisible()
    const option = page.getByRole('option', { name: new RegExp(name) })
    if (name === 'Control plane') {
      await page.keyboard.press('End')
      await page.keyboard.press('Enter')
    } else {
      await option.click()
    }
    await expect(picker).toHaveAttribute(
      'data-context',
      name === 'Control plane' ? 'platform' : 'store',
    )
    await picker.click()
    const selected = page.getByRole('option', { selected: true })
    await expect(selected).toContainText(name)
    await expect(selected.locator('[data-selection-indicator]')).toHaveCount(1)
    await expect(
      page.getByRole('listbox').locator('[data-selection-indicator]'),
    ).toHaveCount(1)
    const popup = (await page.getByRole('listbox').boundingBox())!
    const bounds = (await sidebar.boundingBox())!
    expect(popup.x).toBeGreaterThanOrEqual(bounds.x)
    expect(popup.x + popup.width).toBeLessThanOrEqual(bounds.x + bounds.width)
    if (name === 'Control plane') {
      await expect(picker).toContainText('Platform')
      await page.screenshot({
        path: testInfo.outputPath('platform-context-selector.png'),
        fullPage: true,
      })
    }
    await page.keyboard.press('Escape')
    await expect(picker).toBeFocused()
  }
  await picker.click()
  await page.screenshot({
    path: testInfo.outputPath('compact-context-selector.png'),
    fullPage: true,
  })
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: /Elena Rostova/ }).click()
  await expect(page.getByRole('menuitem', { name: /Log out/ })).toBeDisabled()
  await expect(
    page.getByText('Authentication not connected', { exact: true }),
  ).toBeVisible()
})

test('mobile uses bottom shortcuts and a bottom drawer with scope-aware active states', async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto('/w/studio-berlin/catalog/products')
  const bar = page.getByRole('navigation', { name: 'Quick navigation' })
  await expect(bar).toBeVisible()
  await expect(
    bar.getByRole('link', { name: 'Catalog', exact: true }),
  ).toHaveAttribute('aria-current', 'page')
  await expect(bar.locator('[aria-current="page"]')).toHaveCount(1)
  await bar.getByRole('link', { name: 'Orders', exact: true }).click()
  await expect(page).toHaveURL(/studio-berlin\/orders\/all-orders$/)
  await bar.getByRole('link', { name: 'Changes', exact: true }).click()
  await expect(page).toHaveURL(/studio-berlin\/changes-releases\/my-changes$/)
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.screenshot({
    path: testInfo.outputPath('mobile-bottom-navigation.png'),
    fullPage: false,
  })
  const menu = bar.getByRole('button', { name: 'Open navigation menu' })
  await menu.click()
  const drawer = page.getByRole('dialog', { name: 'Mobile navigation' })
  await expect(drawer).toBeVisible()
  const box = (await drawer.boundingBox())!
  expect(box.x).toBe(0)
  expect(box.width).toBe(390)
  expect(box.y).toBeGreaterThan(0)
  await page.screenshot({
    path: testInfo.outputPath('mobile-bottom-drawer.png'),
    fullPage: false,
  })
  await drawer.getByText('Dark', { exact: true }).click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.screenshot({
    path: testInfo.outputPath('mobile-bottom-drawer-dark.png'),
  })
  await page.keyboard.press('Escape')
  await expect(drawer).toBeHidden()
  await expect(menu).toBeFocused()
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.screenshot({
    path: testInfo.outputPath('mobile-bottom-navigation-dark.png'),
  })
  await page.goto('/platform/policies')
  await expect(
    bar.getByRole('link', { name: 'Policies', exact: true }),
  ).toHaveAttribute('aria-current', 'page')
  await expect(bar.locator('[aria-current="page"]')).toHaveCount(1)
  await expect(
    bar.getByRole('link', { name: 'Catalog', exact: true }),
  ).toHaveCount(0)
  await bar.getByRole('link', { name: 'Resources', exact: true }).click()
  await expect(page).toHaveURL(/platform\/resource-explorer$/)
})
