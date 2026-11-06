import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { destinations, destinationHref } from '../src/navigation/registry'
const home = '/w/atelier-kyoto/home/overview'
test('all 79 destinations are reachable with two mock Home pages and 77 placeholders', async ({
  page,
}) => {
  test.setTimeout(120000)
  const forbidden: string[] = []
  page.on('request', (request) => {
    if (/graphql|\/api\//i.test(request.url())) forbidden.push(request.url())
  })
  for (const destination of destinations) {
    await page.goto(destinationHref(destination, 'atelier-kyoto'))
    await expect(
      page.getByRole('heading', {
        name: destination.label,
        exact: true,
        level: 1,
      }),
    ).toBeVisible()
    if (destination.implementation === 'mock') {
      await expect(page.getByText('Demo data', { exact: true })).toBeVisible()
      if (destination.id === 'home.overview')
        await expect(
          page.getByRole('region', { name: 'Store summary' }),
        ).toBeVisible()
      else
        await expect(
          page.getByRole('tablist', { name: 'My work views' }),
        ).toBeVisible()
    } else {
      await expect(
        page.getByText('PAGE NOT IMPLEMENTED', { exact: true }),
      ).toBeVisible()
    }
  }
  expect(forbidden).toEqual([])
})
test('navigation search, keyboard dismissal, history and reload work', async ({
  page,
}) => {
  await page.goto(home)
  const trigger = page.getByRole('button', {
    name: 'Search navigation',
    exact: true,
  })
  await trigger.click()
  await page
    .getByRole('searchbox', { name: 'Search pages' })
    .fill('catalog variants')
  await page.getByRole('option').click()
  await expect(page).toHaveURL(/catalog\/variants$/)
  await page.reload()
  await expect(
    page.getByRole('heading', { name: 'Variants', level: 1 }),
  ).toBeVisible()
  await page.goBack()
  await expect(
    page.getByRole('heading', { name: 'Overview', level: 1 }),
  ).toBeVisible()
  await page.goForward()
  await expect(
    page.getByRole('heading', { name: 'Variants', level: 1 }),
  ).toBeVisible()
  await trigger.focus()
  await page.keyboard.press('ControlOrMeta+k')
  await expect(
    page.getByRole('dialog', { name: 'Search navigation' }),
  ).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(
    page.getByRole('dialog', { name: 'Search navigation' }),
  ).toBeHidden()
  await expect(trigger).toBeFocused()
})
test('theme, sidebar, profile and workspace context persist or switch correctly', async ({
  page,
}) => {
  await page.goto(home)
  await page.getByText('Dark', { exact: true }).click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  const profile = page.getByRole('button', { name: /Elena Rostova/ })
  await profile.focus()
  await page.keyboard.press('Enter')
  await expect(
    page.getByRole('menuitem', { name: 'Profile & preferences' }),
  ).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(profile).toBeFocused()
  await page.keyboard.press('Enter')
  await page.keyboard.press('Enter')
  await expect(
    page.getByRole('dialog', { name: 'Your workspace preferences' }),
  ).toBeVisible()
  await page
    .getByRole('radiogroup', { name: 'Motion', exact: true })
    .getByText('Reduce motion', { exact: true })
    .click()
  await page.getByRole('button', { name: 'Close preferences' }).click()
  await expect(page.locator('html')).toHaveAttribute('data-motion', 'reduced')
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page
    .getByRole('option', { name: 'Studio Berlin', exact: false })
    .click()
  await expect(page).toHaveURL(/\/w\/studio-berlin\/home\/overview$/)
  await page
    .getByRole('button', { name: 'Collapse sidebar', exact: true })
    .click()
  await page.reload()
  await expect(
    page.getByRole('button', { name: 'Expand sidebar', exact: true }),
  ).toBeVisible()
  await page.getByRole('link', { name: 'Commerce', exact: true }).click()
  await page.getByRole('button', { name: 'Catalog pages', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Products', exact: true }).click()
  await expect(page).toHaveURL(/studio-berlin\/catalog\/products$/)
})
test('layouts, accessibility, responsive overflow and screenshots', async ({
  page,
}, testInfo) => {
  await page.goto(home)
  await expect(
    page.getByRole('navigation', { name: 'Main navigation' }),
  ).toBeVisible()
  await page.evaluate(async () => {
    await document.fonts.ready
    await Promise.all(
      document.getAnimations().map((animation) => animation.finished),
    )
  })
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.screenshot({
    path: testInfo.outputPath('desktop-light.png'),
    fullPage: true,
  })
  await page.getByText('Dark', { exact: true }).click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.evaluate(async () => {
    await Promise.all(
      document.getAnimations().map((animation) => animation.finished),
    )
  })
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.screenshot({
    path: testInfo.outputPath('desktop-dark.png'),
    fullPage: true,
  })
  await page.getByRole('button', { name: 'Inspector', exact: true }).click()
  await expect(
    page.getByRole('button', { name: 'Inspector', exact: true }),
  ).toHaveAttribute('aria-pressed', 'true')
  await expect(
    page.getByRole('complementary', { name: 'Page context' }),
  ).toBeVisible()
  await expect(page.locator('[data-exiting]')).toHaveCount(0)
  await page.screenshot({
    path: testInfo.outputPath('inspector.png'),
    fullPage: true,
  })
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await expect(
    page.getByRole('button', { name: 'Exit Focus Mode' }),
  ).toBeVisible()
  await expect(
    page.getByRole('navigation', { name: 'Main navigation' }),
  ).toHaveCount(0)
  await expect(page.locator('[data-exiting]')).toHaveCount(0)
  await page.screenshot({
    path: testInfo.outputPath('focus.png'),
    fullPage: true,
  })
  await page.getByRole('button', { name: 'Exit Focus Mode' }).click()
  await page.setViewportSize({ width: 900, height: 1000 })
  await expect(
    page.getByRole('link', { name: 'Commerce', exact: true }),
  ).toBeVisible()
  await page.screenshot({
    path: testInfo.outputPath('tablet.png'),
    fullPage: true,
  })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByRole('button', { name: 'Open navigation' }).click()
  await expect(
    page.getByRole('dialog', { name: 'Mobile navigation' }),
  ).toBeVisible()
  await page.screenshot({
    path: testInfo.outputPath('mobile-drawer.png'),
    fullPage: true,
    animations: 'disabled',
  })
  await page.keyboard.press('Escape')
  await expect(
    page.getByRole('dialog', { name: 'Mobile navigation' }),
  ).toBeHidden()
  await expect(
    page.getByRole('button', { name: 'Open navigation' }),
  ).toBeFocused()
  await page.screenshot({
    path: testInfo.outputPath('mobile.png'),
    fullPage: true,
  })
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await expect(page.locator('html')).toHaveAttribute('data-motion', 'reduced')
  expect(
    parseFloat(
      await page
        .locator('main>div')
        .evaluate((el) => getComputedStyle(el).animationDuration),
    ),
  ).toBeLessThanOrEqual(0.001)
  await page.goto('/not-a-route')
  await expect(
    page.getByRole('heading', { name: 'Nothing here, yet.' }),
  ).toBeVisible()
})
test('Storybook renders the component library and inspector layout', async ({
  page,
}) => {
  await page.goto(
    'http://127.0.0.1:6006/iframe.html?id=foundation-controls--light&viewMode=story',
  )
  await expect(
    page.getByRole('heading', { name: 'GitStore / shared controls' }),
  ).toBeVisible()
  await page.goto(
    'http://127.0.0.1:6006/iframe.html?id=foundation-layouts--with-inspector&viewMode=story',
  )
  await expect(
    page.getByRole('heading', { name: 'Products', level: 1 }),
  ).toBeVisible()
  await expect(
    page.getByRole('complementary', { name: 'Page context' }),
  ).toBeVisible()
})
