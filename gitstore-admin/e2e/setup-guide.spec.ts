import { test, expect } from '@playwright/test'
import type { Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { openDemoReset } from './home-interactions'
import { storybookURL } from './storybook-url'
const kyoto = '/w/atelier-kyoto/home/setup-guide'
const berlin = '/w/studio-berlin/home/setup-guide'
const progress = (page: Page) =>
  page.getByRole('progressbar', { name: 'Demo setup progress' })
async function command(page: Page, title: string) {
  await page.keyboard.press('ControlOrMeta+k')
  await page.getByRole('searchbox', { name: 'Search pages' }).fill(title)
  await page.getByRole('option', { name: `${title} Home`, exact: true }).click()
  await expect(
    page.getByRole('heading', { level: 1, name: title, exact: true }),
  ).toBeVisible()
}
async function review(page: Page, confirm = true) {
  await page
    .getByRole('button', { name: 'Mark reviewed in demo', exact: true })
    .click()
  const dialog = page.getByRole('dialog', {
    name: 'Mark this step reviewed in demo?',
  })
  await expect(dialog).toContainText(
    'No settings, categories, or resource conditions are changed',
  )
  await dialog
    .getByRole('button', {
      name: confirm ? 'Confirm demo review' : 'Cancel',
      exact: true,
    })
    .click()
}
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
})

test('guidance confirmations, cancellation, reopening and shared Overview progress use real task links', async ({
  page,
}, testInfo) => {
  const backend: string[] = []
  page.on('request', (req) => {
    if (/graphql|\/api\//i.test(req.url())) backend.push(req.url())
  })
  await page.goto(kyoto)
  await expect(page).toHaveTitle('Setup guide · GitStore')
  await expect(progress(page)).toHaveAttribute('value', '3')
  await page.getByRole('button', { name: 'Continue setup' }).click()
  await expect(page).toHaveURL(/step=catalog-checks/)
  await expect(
    page.getByRole('heading', { name: 'Review catalog checks', exact: true }),
  ).toBeFocused()
  await review(page, false)
  await expect(progress(page)).toHaveAttribute('value', '3')
  await expect(
    page.getByRole('button', { name: 'Mark reviewed in demo', exact: true }),
  ).toBeFocused()
  await review(page)
  await expect(progress(page)).toHaveAttribute('value', '4')
  await expect(
    page.getByRole('heading', { name: 'Checklist complete' }),
  ).toBeVisible()
  await expect(
    page.getByRole('heading', { name: 'Review catalog checks', exact: true }),
  ).toBeFocused()
  await expect(page.getByRole('status')).toContainText(
    'Only your demo checklist changed',
  )
  await page.screenshot({
    path: testInfo.outputPath('setup-complete.png'),
    fullPage: true,
  })
  await page.getByRole('button', { name: 'Mark not reviewed' }).click()
  await expect(progress(page)).toHaveAttribute('value', '3')
  await command(page, 'Overview')
  const summary = page.getByRole('region', { name: 'Store setup' })
  await expect(summary.getByRole('progressbar')).toHaveAttribute('value', '3')
  await summary.getByRole('link', { name: 'Review catalog checks' }).click()
  await expect(
    page.getByRole('heading', { name: 'Review catalog checks', exact: true }),
  ).toBeVisible()
  await review(page)
  await command(page, 'Recent activity')
  await expect(
    page.getByText('10 activities shown', { exact: true }),
  ).toBeVisible()
  await expect(
    page.getByText('DEMO SESSION CHANGE', { exact: true }),
  ).toHaveCount(0)
  await page.goBack()
  await expect(progress(page)).toHaveAttribute('value', '4')
  expect(backend).toEqual([])
})

test('task deep links, browser history, close replacement and inspector cleanup preserve focus', async ({
  page,
}) => {
  await page.goto(`${kyoto}?step=categories`)
  await expect(
    page.getByRole('heading', { name: 'Review categories', exact: true }),
  ).toBeFocused()
  await expect(
    page.getByText('Page not implemented', { exact: true }),
  ).toBeVisible()
  await page
    .getByRole('button', { name: 'Close inspector', exact: true })
    .click()
  await expect(page).not.toHaveURL(/step=/)
  await expect(
    page.getByRole('button', {
      name: /^Review guidance: Review categories(?: |$)/,
    }),
  ).toBeFocused()
  await page
    .getByRole('button', {
      name: /^Review guidance: Review store details(?: |$)/,
    })
    .click()
  await expect(
    page.getByRole('heading', { name: 'Review store details', exact: true }),
  ).toBeFocused()
  await page
    .getByRole('button', {
      name: /^Review guidance: Review categories(?: |$)/,
    })
    .click()
  await page.goBack()
  await expect(page).toHaveURL(/step=store-details/)
  await expect(
    page.getByRole('heading', { name: 'Review store details', exact: true }),
  ).toBeFocused()
  await page.goForward()
  await expect(page).toHaveURL(/step=categories/)
  await expect(
    page.getByRole('heading', { name: 'Review categories', exact: true }),
  ).toBeFocused()
  await page.getByRole('link', { name: 'Open categories', exact: true }).click()
  await expect(
    page.getByText('PAGE NOT IMPLEMENTED', { exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('heading', { name: 'Review categories', exact: true }),
  ).toHaveCount(0)
  await page.goto(`${kyoto}?step=not-a-task`)
  await expect(progress(page)).toHaveAttribute('value', '3')
  await expect(
    page.getByRole('heading', { name: 'Review categories', exact: true }),
  ).toHaveCount(0)
})

test('store sessions, Focus, reload and confirmed reset retain the right review state', async ({
  page,
}) => {
  await page.goto(berlin)
  await expect(progress(page)).toHaveAttribute('value', '0')
  await page.getByRole('button', { name: 'Continue setup' }).click()
  await review(page)
  await expect(progress(page)).toHaveAttribute('value', '1')
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await expect(progress(page)).toHaveAttribute('value', '1')
  await expect(page).toHaveURL(/step=store-details/)
  await page
    .getByRole('button', { name: 'Exit Focus Mode', exact: true })
    .click()
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Atelier Kyoto/ }).click()
  await expect(progress(page)).toHaveAttribute('value', '3')
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Studio Berlin/ }).click()
  await expect(progress(page)).toHaveAttribute('value', '1')
  await page.reload()
  await expect(progress(page)).toHaveAttribute('value', '0')
  await page.getByRole('button', { name: 'Continue setup' }).click()
  await review(page)
  await page
    .getByRole('button', { name: 'Close inspector', exact: true })
    .click()
  await openDemoReset(page)
  await page
    .getByRole('dialog', { name: /Reset catalog and Home demo/ })
    .getByRole('button', { name: 'Cancel', exact: true })
    .click()
  await expect(progress(page)).toHaveAttribute('value', '1')
  await openDemoReset(page)
  await page
    .getByRole('button', { name: 'Confirm demo reset', exact: true })
    .click()
  await expect(progress(page)).toHaveAttribute('value', '0')
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Atelier Kyoto/ }).click()
  await expect(progress(page)).toHaveAttribute('value', '3')
})

test('saving a retired processing Product completes setup; starting and finishing deletion reopen it', async ({
  page,
}) => {
  await page.goto(`${berlin}?step=first-product`)
  await expect(progress(page)).toHaveAttribute('value', '0')
  await page.getByRole('link', { name: 'Add demo product' }).click()
  await page
    .getByRole('textbox', { name: 'Title', exact: true })
    .fill('Guide product')
  await page.getByRole('button', { name: /Lifecycle$/ }).click()
  await page.getByRole('option', { name: 'Retired', exact: true }).click()
  await page
    .getByRole('button', { name: 'Create demo product', exact: true })
    .click()
  await expect(
    page.getByRole('heading', { name: 'Guide product', level: 1, exact: true }),
  ).toBeVisible()
  await command(page, 'Setup guide')
  await expect(progress(page)).toHaveAttribute('value', '1')
  await page
    .getByRole('button', {
      name: /^Review guidance: Add your first product(?: |$)/,
    })
    .click()
  await expect(
    page.getByText('Product added in demo', { exact: true }),
  ).toBeVisible()
  await page.getByRole('link', { name: 'View products', exact: true }).click()
  await page.getByRole('link', { name: /^View Guide product(?: |$)/ }).click()
  await page
    .getByRole('button', { name: 'Delete demo product', exact: true })
    .click()
  await page
    .getByRole('button', { name: 'Confirm demo deletion', exact: true })
    .click()
  await expect(
    page.getByRole('heading', { name: 'Deletion in progress' }),
  ).toBeVisible()
  await command(page, 'Setup guide')
  await expect(progress(page)).toHaveAttribute('value', '0')
  await page
    .getByRole('button', {
      name: /^Review guidance: Add your first product(?: |$)/,
    })
    .click()
  await page.getByRole('link', { name: 'View products', exact: true }).click()
  await page.getByRole('link', { name: /^View Guide product(?: |$)/ }).click()
  await page
    .getByRole('button', { name: 'Complete demo deletion', exact: true })
    .click()
  await page
    .getByRole('button', { name: 'Confirm completion', exact: true })
    .click()
  await command(page, 'Setup guide')
  await expect(progress(page)).toHaveAttribute('value', '0')
})

for (const [width, theme] of [
  [1440, 'light'],
  [1440, 'dark'],
  [900, 'light'],
  [375, 'light'],
  [375, 'dark'],
] as const)
  test(`setup is accessible and its next action and confirmation stay visible at ${width}px in ${theme}`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: width === 375 ? 667 : 900 })
    await page.addInitScript(
      (theme) => localStorage.setItem('gitstore.theme', theme),
      theme,
    )
    await page.goto(width === 375 ? berlin : kyoto)
    const next = page.getByRole('button', { name: 'Continue setup' })
    await expect(next).toBeVisible()
    const box = (await next.boundingBox())!
    expect(box.y + box.height).toBeLessThan(width === 375 ? 600 : 800)
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.screenshot({
      path: testInfo.outputPath(`setup-${width}-${theme}.png`),
      fullPage: true,
    })
    await next.click()
    const title =
      width === 375 ? 'Review store details' : 'Review catalog checks'
    await expect(
      page.getByRole('heading', { name: title, exact: true }),
    ).toBeFocused()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.screenshot({
      path: testInfo.outputPath(`setup-inspector-${width}-${theme}.png`),
    })
    await page
      .getByRole('button', { name: 'Mark reviewed in demo', exact: true })
      .click()
    const dialog = page.getByRole('dialog', {
      name: 'Mark this step reviewed in demo?',
    })
    const confirm = dialog.getByRole('button', {
      name: 'Confirm demo review',
      exact: true,
    })
    await expect(confirm).toBeVisible()
    expect(
      await confirm.evaluate((node) => {
        const r = node.getBoundingClientRect()
        return node.contains(
          document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2),
        )
      }),
    ).toBe(true)
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.screenshot({
      path: testInfo.outputPath(`setup-confirmation-${width}-${theme}.png`),
    })
    await confirm.click()
    await expect(progress(page)).toHaveAttribute(
      'value',
      width === 375 ? '1' : '4',
    )
    await expect(
      page.getByRole('heading', { name: title, exact: true }),
    ).toBeFocused()
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
    await page
      .getByRole('button', { name: 'Close inspector', exact: true })
      .click()
    await expect(page).not.toHaveURL(/step=/)
  })

for (const width of [1440, 981])
  for (const story of ['confirmation', 'reviewed', 'product-deleting'])
    test(`Setup guide ${story} story completes its interaction at ${width}px`, async ({
      page,
    }) => {
      await page.setViewportSize({ width, height: 900 })
      await page.goto(
        `${storybookURL}/?path=/story/pages-home-setup-guide--${story}`,
      )
      await page.getByRole('tab', { name: /^Interactions/ }).click()
      await expect(
        page
          .getByRole('toolbar', { name: 'Component test playback controls' })
          .getByText(/^pass$/i),
      ).toBeVisible({ timeout: 20000 })
    })

test('setup presentation specimens expose their intended state without backend behavior', async ({
  page,
}) => {
  for (const [story, check] of [
    ['complete', 'Checklist complete'],
    ['loading', 'Loading setup guide'],
    ['unavailable', 'Setup guide unavailable'],
    ['reopened-review', 'Review categories'],
  ] as const) {
    await page.goto(
      `${storybookURL}/iframe.html?id=pages-home-setup-guide--${story}&viewMode=story`,
    )
    await expect(
      page.getByRole('heading', { name: check, exact: true }),
    ).toBeVisible()
  }
  await page.goto(
    `${storybookURL}/iframe.html?id=pages-home-setup-guide--read-only&viewMode=story`,
  )
  await expect(
    page.getByRole('button', { name: 'Mark reviewed in demo', exact: true }),
  ).toBeDisabled()
  await expect(page.getByText(/Read-only preview/).first()).toBeVisible()
  await page.goto(
    `${storybookURL}/iframe.html?id=pages-home-setup-guide--reduced-motion&viewMode=story`,
  )
  await expect(page.locator('html')).toHaveAttribute('data-motion', 'reduced')
  await expect(progress(page)).toHaveAttribute('value', '3')
})

for (const width of [1440, 900, 375]) {
  test(`selecting and dismissing setup guidance preserves page scroll at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 667 })
    await page.goto(kyoto)
    const task = page.getByRole('button', {
      name: /^Review guidance: Review categories(?: |$)/,
    })
    await task.evaluate((element) => element.scrollIntoView({ block: 'start' }))
    await page.evaluate(() => window.scrollBy(0, -120))
    const scroll = await page.evaluate(() => window.scrollY)
    expect(scroll).toBeGreaterThan(0)
    await task.click()
    await expect(
      page.getByRole('heading', { name: 'Review categories', exact: true }),
    ).toBeFocused()
    if (width > 1100) {
      await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(scroll)
    }
    await page
      .getByRole('button', { name: 'Close inspector', exact: true })
      .click()
    await expect(task).toBeFocused()
    await expect(page).not.toHaveURL(/step=/)
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(scroll)
  })
}
