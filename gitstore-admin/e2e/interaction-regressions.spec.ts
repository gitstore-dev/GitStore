import { test, expect } from '@playwright/test'

test('keyboard focus rings fit inside the sidebar navigation scrollport', async ({
  page,
}, testInfo) => {
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme })
    for (const destination of [
      { path: 'home/overview', label: 'Recent activity' },
      { path: 'pricing-promotions/promotions', label: 'Discount codes' },
    ]) {
      await page.goto(`/w/atelier-kyoto/${destination.path}`)
      const nav = page.getByRole('navigation', {
        name: 'Main navigation',
        exact: true,
      })
      const link = nav.getByRole('link', {
        name: destination.label,
        exact: true,
      })
      await link.focus()
      await page.keyboard.press('ArrowUp')
      await page.keyboard.press('ArrowDown')
      await expect(link).toBeFocused()
      const ring = await link.evaluate((element) => {
        const style = getComputedStyle(element)
        const bounds = element.getBoundingClientRect()
        const nav = element.closest('nav')!
        const viewport = nav.getBoundingClientRect()
        const extent =
          parseFloat(style.outlineWidth) + parseFloat(style.outlineOffset)
        return {
          visible: element.matches(':focus-visible'),
          width: parseFloat(style.outlineWidth),
          left: bounds.left - extent,
          right: bounds.right + extent,
          viewportLeft: viewport.left,
          viewportRight: viewport.left + nav.clientWidth,
        }
      })
      expect(ring.visible).toBe(true)
      expect(ring.width).toBeGreaterThanOrEqual(2)
      expect(ring.left).toBeGreaterThanOrEqual(ring.viewportLeft)
      expect(ring.right).toBeLessThanOrEqual(ring.viewportRight)
      await page.screenshot({
        path: testInfo.outputPath(
          `sidebar-focus-${theme}-${destination.label}.png`,
        ),
      })
    }
  }
})

test('layout icons expose tooltips, keyboard selection, and focus restoration', async ({
  page,
}, testInfo) => {
  await page.goto('/w/atelier-kyoto/home/overview')
  const inspector = page.getByRole('button', { name: 'Inspector', exact: true })
  const focus = page.getByRole('button', { name: 'Focus mode', exact: true })
  await expect(
    page.getByRole('group', { name: 'View controls' }).getByRole('button'),
  ).toHaveCount(2)
  await expect(inspector).toHaveAttribute('aria-pressed', 'false')
  await expect(focus).toHaveAttribute('aria-pressed', 'false')
  await inspector.focus()
  await expect(page.getByRole('tooltip')).toHaveText('Inspector')
  await inspector.press('Space')
  await expect(inspector).toHaveAttribute('aria-pressed', 'true')
  await expect(
    page.getByRole('complementary', { name: 'Page context' }),
  ).toBeVisible()
  await inspector.press('Space')
  await expect(inspector).toHaveAttribute('aria-pressed', 'false')
  await expect(
    page.getByRole('complementary', { name: 'Page context' }),
  ).toBeHidden()
  await page.screenshot({ path: testInfo.outputPath('layout-icons.png') })
  await focus.press('Space')
  const exit = page.getByRole('button', { name: 'Exit Focus Mode' })
  await expect(exit).toBeFocused()
  await exit.press('Enter')
  await expect(focus).toHaveAttribute('aria-pressed', 'false')
  await expect(focus).toBeFocused()
})

test('sidebar arrows move focus without navigation; Enter opens the focused destination', async ({
  page,
}) => {
  await page.goto('/w/atelier-kyoto/pricing-promotions/promotions')
  const nav = page.getByRole('navigation', {
    name: 'Main navigation',
    exact: true,
  })
  const priceLists = nav.getByRole('link', { name: 'Price lists', exact: true })
  await priceLists.focus()
  await page.keyboard.press('ArrowDown')
  await expect(
    nav.getByRole('link', { name: 'Promotions', exact: true }),
  ).toBeFocused()
  await page.keyboard.press('ArrowDown')
  await expect(
    nav.getByRole('link', { name: 'Discount codes', exact: true }),
  ).toBeFocused()
  await expect(page).toHaveURL(/pricing-promotions\/promotions$/)
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/pricing-promotions\/discount-codes$/)
  await expect(
    page.getByRole('heading', { name: 'Discount codes', level: 1 }),
  ).toBeVisible()
  await page.keyboard.press('ArrowUp')
  await expect(
    nav.getByRole('link', { name: 'Promotions', exact: true }),
  ).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/pricing-promotions\/promotions$/)
  await page.keyboard.press('Home')
  await expect(
    nav.getByRole('link', { name: 'Back to workspace', exact: true }),
  ).toBeFocused()
  await page.keyboard.press('End')
  await expect(
    nav.getByRole('link', { name: 'Store credit', exact: true }),
  ).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/pricing-promotions\/store-credit$/)
})

test('rail menu keyboard activation navigates rather than only focusing an item', async ({
  page,
}) => {
  await page.setViewportSize({ width: 900, height: 900 })
  await page.goto('/w/atelier-kyoto/pricing-promotions/promotions')
  const trigger = page.getByRole('button', {
    name: 'Pricing & promotions pages',
    exact: true,
  })
  await trigger.focus()
  await page.keyboard.press('Enter')
  await expect(
    page.getByRole('menuitem', { name: 'Price lists', exact: true }),
  ).toBeFocused()
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('ArrowDown')
  await expect(
    page.getByRole('menuitem', { name: 'Discount codes', exact: true }),
  ).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/pricing-promotions\/discount-codes$/)
})

for (const viewport of [
  { width: 430, height: 932 },
  { width: 320, height: 568 },
]) {
  test(`profile popup stays visible above the mobile drawer footer at ${viewport.width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(viewport)
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto('/w/studio-berlin/settings/store-details')
    const drawerTrigger = page.getByRole('button', {
      name: 'Open navigation menu',
    })
    await drawerTrigger.click()
    const drawer = page.getByRole('dialog', { name: 'Mobile navigation' })
    const profile = drawer.getByRole('button', { name: /Elena Rostova/ })
    await profile.click()
    const menu = page.getByRole('menu').filter({
      has: page.getByRole('menuitem', {
        name: 'Profile & preferences',
        exact: true,
      }),
    })
    await expect(menu).toBeVisible()
    const bounds = (await menu.boundingBox())!
    const triggerBounds = (await profile.boundingBox())!
    expect(bounds.x).toBeGreaterThanOrEqual(0)
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(viewport.width)
    expect(bounds.y).toBeGreaterThanOrEqual(0)
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(triggerBounds.y)
    await page.screenshot({
      path: testInfo.outputPath('mobile-profile-menu.png'),
    })
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()
    await expect(drawer).toBeVisible()
    await expect(profile).toBeFocused()
    await profile.press('Enter')
    await page
      .getByRole('menuitem', { name: 'Profile & preferences', exact: true })
      .press('Enter')
    await expect(
      page.getByRole('dialog', { name: 'Your workspace preferences' }),
    ).toBeVisible()
  })
}
