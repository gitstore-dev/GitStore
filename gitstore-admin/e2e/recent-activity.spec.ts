import { openDemoReset } from './home-interactions'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
const activity = '/w/atelier-kyoto/home/recent-activity'
const nav = (page: Page) =>
  page.getByRole('navigation', { name: 'Main navigation', exact: true })
async function selectOption(page: Page, label: string, value: string) {
  await expect(
    page.getByRole('heading', {
      name: 'Recent activity',
      level: 1,
      exact: true,
    }),
  ).toBeVisible()
  const button = page.getByRole('button', { name: new RegExp(`${label}$`) })
  const mobile = !(await button.isVisible())
  if (mobile) await page.getByRole('button', { name: /^Filters/ }).click()
  await button.click()
  await page.getByRole('option', { name: value, exact: true }).click()
  if (mobile)
    await page
      .getByRole('dialog', { name: 'Filter activity' })
      .getByRole('button', { name: 'Apply filters' })
      .click()
}
async function oldBasin(page: Page) {
  await page.getByRole('searchbox', { name: 'Search activity' }).fill('Kanso')
  await selectOption(page, 'Recorded date', '31 Oct 2026')
  await page
    .getByRole('button', {
      name: 'View details for Kanso Stone Basin — Original',
      exact: true,
    })
    .click()
}
const panel = (page: Page) =>
  page.getByRole('complementary', { name: 'Page context' })
async function recover(page: Page) {
  await page
    .getByRole('button', { name: 'Restore this version in demo', exact: true })
    .click()
  await page
    .getByRole('dialog', { name: 'Restore this version in demo?' })
    .getByRole('button', { name: 'Confirm demo recovery' })
    .click()
}
test('timeline filters, dates, pagination and history are local and merchant-facing', async ({
  page,
}, testInfo) => {
  const backend: string[] = []
  page.on('request', (request) => {
    if (/graphql|\/api\//i.test(request.url())) backend.push(request.url())
  })
  await page.goto(activity)
  await expect(page).toHaveTitle('Recent activity · GitStore')
  await expect(page.getByText('10 activities shown')).toBeVisible()
  await expect(page.locator('[data-resource-id]')).toHaveCount(10)
  await page.getByRole('button', { name: 'Load more' }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(20)
  await expect(
    page.getByText('20 activities shown', { exact: true }),
  ).toBeVisible()
  await expect(page.getByRole('status')).toContainText(
    'Loaded 10 more activities. 20 activities shown.',
  )
  await page.getByRole('button', { name: 'Load more' }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(24)
  await expect(
    page.getByText('24 activities shown', { exact: true }),
  ).toBeVisible()
  await expect(page.getByRole('status')).toContainText(
    'Loaded 4 more activities. 24 activities shown.',
  )
  await expect(page.getByRole('button', { name: 'Load more' })).toHaveCount(0)
  await selectOption(page, 'Actor', 'Kenji Sato')
  await selectOption(page, 'Item type', 'Category')
  await selectOption(page, 'Event category', 'Catalog changes')
  await selectOption(page, 'Recorded date', '31 Oct 2026')
  await expect(page.locator('[data-resource-id]')).toHaveCount(1)
  await expect(
    page.getByText('1 matching activity shown', { exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'View details for Homeware — Original' }),
  ).toBeVisible()
  await page
    .getByRole('searchbox', { name: 'Search activity' })
    .fill('no matching title')
  await expect(
    page.getByRole('heading', { name: 'No matching activity' }),
  ).toBeVisible()
  await page
    .getByRole('button', { name: 'Clear filters', exact: true })
    .first()
    .click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(10)
  await page.screenshot({
    path: testInfo.outputPath('recent-activity-populated.png'),
  })
  expect(backend).toEqual([])
})
test('recovery preserves draft buffers, updates Overview and Completed work, and persists across stores', async ({
  page,
}, testInfo) => {
  await page.goto('/w/atelier-kyoto/home/my-work')
  await page
    .getByRole('button', {
      name: 'View details for Kanso Stone Basin',
      exact: true,
    })
    .click()
  await page
    .getByRole('button', { name: 'Edit demo draft', exact: true })
    .click()
  await page
    .getByRole('textbox', { name: 'Title', exact: true })
    .fill('My unsaved basin title')
  await nav(page)
    .getByRole('link', { name: 'Recent activity', exact: true })
    .click()
  await oldBasin(page)
  await expect(
    panel(page).getByRole('region', { name: 'Recorded changes' }),
  ).toContainText('Title')
  await page
    .getByRole('button', { name: 'Restore this version in demo', exact: true })
    .click()
  const confirmation = page.getByRole('dialog', {
    name: 'Restore this version in demo?',
  })
  await expect(
    confirmation.getByRole('region', { name: 'Proposed recovery' }),
  ).toContainText('Product images')
  await confirmation
    .getByRole('button', { name: 'Cancel', exact: true })
    .click()
  await expect(
    page.getByRole('button', {
      name: 'Restore this version in demo',
      exact: true,
    }),
  ).toBeFocused()
  await expect(
    page.getByText('1 matching activity shown', { exact: true }),
  ).toBeVisible()
  await recover(page)
  await expect(
    panel(page).getByRole('heading', {
      name: 'Kanso Stone Basin — Original',
      exact: true,
    }),
  ).toBeFocused()
  await expect(
    page.getByRole('button', {
      name: 'Restore this version in demo',
      exact: true,
    }),
  ).toBeDisabled()
  await expect(page.getByRole('status')).toContainText(
    'Nothing was published or marked ready',
  )
  await page.getByRole('button', { name: 'Close inspector' }).click()
  await page.getByRole('button', { name: 'View recovery event' }).click()
  await expect(panel(page)).toContainText('Version restored in demo')
  await page.screenshot({
    path: testInfo.outputPath('recent-activity-recovery-inspector.png'),
  })
  await page.getByRole('button', { name: 'Close inspector' }).click()
  await nav(page).getByRole('link', { name: 'Overview', exact: true }).click()
  await expect(
    page.getByRole('button', {
      name: 'View details for Kanso Stone Basin — Original',
      exact: true,
    }),
  ).toBeVisible()
  const summary = page.getByRole('region', { name: 'Store summary' })
  await expect(
    summary.getByRole('article').filter({ hasText: 'Needs attention' }),
  ).toContainText('4')
  await expect(
    page.getByRole('button', {
      name: /Version restored in demo: Kanso Stone Basin/,
    }),
  ).toBeVisible()
  await nav(page).getByRole('link', { name: 'My work', exact: true }).click()
  await page
    .getByRole('button', {
      name: 'View details for Kanso Stone Basin',
      exact: true,
    })
    .click()
  await expect(
    page.getByRole('textbox', { name: 'Title', exact: true }),
  ).toHaveValue('My unsaved basin title')
  await expect(panel(page)).toContainText('based on an earlier version')
  await page.getByRole('button', { name: 'Save demo draft' }).click()
  await page.getByRole('tab', { name: /^Completed/ }).click()
  await page
    .getByRole('button', {
      name: 'View details for Kanso Stone Basin — Original',
    })
    .click()
  await expect(panel(page)).toContainText('Restored in demo')
  await expect(
    panel(page).getByRole('region', { name: 'Change preview' }),
  ).toContainText('Product images')
  await page.getByRole('button', { name: 'Close inspector' }).click()
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Studio Berlin/ }).click()
  await expect(page.getByRole('heading', { name: 'No work yet' })).toBeVisible()
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Atelier Kyoto/ }).click()
  await expect(page.getByRole('tab', { name: /^Completed 2/ })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('tab', { name: /^Completed 1/ })).toBeVisible()
})
test('all four authored kinds can be recovered into read-only completed work', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto(activity)
  for (const [title, kind] of [
    ['Kanso Stone Basin — Original', 'Product'],
    ['Homeware — Original', 'CategoryTaxonomy'],
    ['Autumn Objects — Original', 'Collection'],
    ['Original basin-detail.webp', 'File'],
  ]) {
    await selectOption(page, 'Recorded date', '31 Oct 2026')
    await page.getByRole('searchbox', { name: 'Search activity' }).fill(title!)
    await page
      .getByRole('button', { name: `View details for ${title}`, exact: true })
      .click()
    await recover(page)
    await page.getByRole('button', { name: 'Close inspector' }).click()
    await nav(page).getByRole('link', { name: 'My work', exact: true }).click()
    await page.getByRole('tab', { name: /^Completed/ }).click()
    await page
      .getByRole('button', { name: `View details for ${title}`, exact: true })
      .click()
    await expect(panel(page)).toContainText('No review was performed')
    await expect(
      panel(page).getByRole('button', { name: 'Edit demo draft', exact: true }),
    ).toHaveCount(0)
    await panel(page).getByText('Technical details', { exact: true }).click()
    await expect(panel(page).locator('dd').last()).toHaveText(kind!)
    await page.getByRole('button', { name: 'Close inspector' }).click()
    await nav(page)
      .getByRole('link', { name: 'Recent activity', exact: true })
      .click()
  }
})
test('saved work actions and resolution append shared history; reset clears all Home state for the active store', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto('/w/atelier-kyoto/home/overview')
  await page
    .getByRole('button', {
      name: 'View details for Kanso Stone Basin',
      exact: true,
    })
    .click()
  await page.getByRole('button', { name: 'Apply demo fix' }).click()
  await page.getByRole('button', { name: 'Confirm demo change' }).click()
  await page.getByRole('button', { name: 'Close inspector' }).click()
  await nav(page).getByRole('link', { name: 'My work', exact: true }).click()
  await page
    .getByRole('button', {
      name: 'View details for Kanso Stone Basin',
      exact: true,
    })
    .click()
  await page
    .getByRole('button', { name: 'Edit demo draft', exact: true })
    .click()
  await page
    .getByRole('textbox', { name: 'Title', exact: true })
    .fill('Saved demo title')
  await page.getByRole('button', { name: 'Save demo draft' }).click()
  await page.getByRole('button', { name: 'Submit demo draft' }).click()
  await page.getByRole('button', { name: 'Confirm demo action' }).click()
  await page.getByRole('button', { name: 'Close inspector' }).click()
  await nav(page)
    .getByRole('link', { name: 'Recent activity', exact: true })
    .click()
  await expect(
    page.getByText('10 activities shown', { exact: true }),
  ).toBeVisible()
  await expect(
    page.getByText('Draft submitted', { exact: true }).first(),
  ).toBeVisible()
  await selectOption(page, 'Event category', 'Processing')
  await page
    .getByRole('button', {
      name: 'View details for Kanso Stone Basin',
      exact: true,
    })
    .click()
  await expect(
    panel(page).getByRole('button', { name: 'Restore this version in demo' }),
  ).toHaveCount(0)
  await openDemoReset(page)
  const reset = page.getByRole('dialog', {
    name: 'Reset catalog and Home demo for Atelier Kyoto?',
  })
  await reset.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(
    page.getByText(
      `${await page.locator('[data-resource-id]').count()} matching activities shown`,
      { exact: true },
    ),
  ).toBeVisible()
  await openDemoReset(page)
  await reset.getByRole('button', { name: 'Confirm demo reset' }).click()
  await expect(
    page.getByRole('heading', { name: 'Recent activity', exact: true }),
  ).toBeFocused()
  await expect(page.getByText('10 activities shown')).toBeVisible()
  await expect(panel(page)).toBeHidden()
  await nav(page).getByRole('link', { name: 'My work', exact: true }).click()
  await expect(page.getByRole('tab', { name: /^Drafts 2/ })).toBeVisible()
})
for (const width of [1440, 900, 390]) {
  test(`activity and recovery are accessible and unobstructed at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto(activity)
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await oldBasin(page)
    const inspector =
      width > 1100
        ? panel(page)
        : page.getByRole('dialog', { name: 'Page context', exact: true })
    await expect(inspector).toBeVisible()
    await page
      .getByRole('button', {
        name: 'Restore this version in demo',
        exact: true,
      })
      .click()
    const dialog = page.getByRole('dialog', {
      name: 'Restore this version in demo?',
    })
    const confirm = dialog.getByRole('button', {
      name: 'Confirm demo recovery',
    })
    await confirm.scrollIntoViewIfNeeded()
    expect(
      await confirm.evaluate((element) => {
        const r = element.getBoundingClientRect()
        return element.contains(
          document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2),
        )
      }),
    ).toBe(true)
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
    await page.screenshot({
      path: testInfo.outputPath(`recent-activity-recovery-${width}.png`),
    })
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(
      inspector.getByRole('button', {
        name: 'Restore this version in demo',
        exact: true,
      }),
    ).toBeFocused()
    await page.getByRole('button', { name: 'Close inspector' }).click()
    await page.emulateMedia({ colorScheme: 'dark' })
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.screenshot({
      path: testInfo.outputPath(`recent-activity-dark-${width}.png`),
    })
    await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
    await expect(
      page.getByRole('searchbox', { name: 'Search activity' }),
    ).toHaveValue('Kanso')
    await page.getByRole('button', { name: 'Exit Focus Mode' }).click()
  })
}
