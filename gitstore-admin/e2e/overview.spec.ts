import { openDemoReset } from './home-interactions'
import { storybookURL } from './storybook-url'
import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
const home = '/w/atelier-kyoto/home/overview'
function card(page: import('@playwright/test').Page, label: string) {
  return page
    .getByRole('region', { name: 'Store summary' })
    .getByRole('article')
    .filter({ hasText: label })
}
test('Overview derives counts, filters and sorts resource work without backend requests', async ({
  page,
}) => {
  const requests: string[] = []
  page.on('request', (request) => {
    if (/graphql|\/api\//i.test(request.url())) requests.push(request.url())
  })
  await page.goto(home)
  await expect(
    page.getByRole('heading', { name: 'Overview', exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('heading', { name: 'Updates and issues' }),
  ).toBeVisible()
  await expect(
    page.getByRole('heading', { name: 'Store status', exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('heading', { name: 'Store setup', exact: true }),
  ).toBeVisible()
  await expect(card(page, 'Total items')).toContainText('18')
  await expect(card(page, 'Needs attention')).toContainText('4')
  await expect(
    page.getByRole('button', { name: /^View details for / }),
  ).toHaveCount(6)
  await page.getByRole('radio', { name: /Has errors/ }).press('Space')
  await expect(
    page.getByRole('button', { name: /^View details for / }),
  ).toHaveCount(1)
  await expect(
    page.getByRole('button', {
      name: 'View details for Raw Silk Tunic — Natural / M',
    }),
  ).toBeVisible()
  await page.getByRole('button', { name: 'Clear filters' }).click()
  await page.getByRole('button', { name: /Sort by/ }).click()
  await page.getByRole('option', { name: 'Title A–Z' }).click()
  await expect(
    page.getByRole('button', { name: /^View details for / }).first(),
  ).toHaveAccessibleName('View details for Autumn Objects')
  expect(requests).toEqual([])
})

test('resource inspection, cancellation, demo resolution, activity, ready inspection and reset work', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto(home)
  await page
    .getByRole('button', { name: 'View details for Kanso Stone Basin' })
    .click()
  const panel = page.getByRole('complementary', { name: 'Page context' })
  await expect(
    panel.getByRole('heading', { name: 'Kanso Stone Basin' }),
  ).toBeVisible()
  await expect(
    panel.getByText('catalog/products/kanso-stone-basin.md', { exact: true }),
  ).toBeHidden()
  await panel.getByText('Technical details', { exact: true }).click()
  await expect(
    panel.locator('details').getByText('Product', { exact: true }),
  ).toBeVisible()
  await expect(panel).toContainText('catalog/products/kanso-stone-basin.md')
  await panel.getByRole('button', { name: 'Apply demo fix' }).click()
  const confirmation = page.getByRole('dialog', { name: 'Apply demo fix?' })
  await expect(confirmation).toBeVisible()
  await confirmation
    .getByRole('button', { name: 'Cancel', exact: true })
    .click()
  await expect(card(page, 'Needs attention')).toContainText('4')
  await expect(
    page.getByRole('button', { name: 'View details for Kanso Stone Basin' }),
  ).toBeVisible()
  await panel.getByRole('button', { name: 'Apply demo fix' }).click()
  await confirmation
    .getByRole('button', { name: 'Confirm demo change' })
    .click()
  await expect(
    panel.getByRole('heading', { name: 'Kanso Stone Basin' }),
  ).toBeFocused()
  await expect(page.getByRole('status')).toContainText('Demo change applied')
  await expect(card(page, 'Needs attention')).toContainText('3')
  await expect(card(page, 'Ready')).toContainText('13')
  await expect(
    page.getByRole('button', { name: 'View details for Kanso Stone Basin' }),
  ).toHaveCount(0)
  await expect(panel).toContainText('No demo action needed')
  await page
    .getByRole('button', { name: 'Close inspector', exact: true })
    .click()
  await page
    .getByRole('button', {
      name: 'Demo processing completed: Kanso Stone Basin',
    })
    .click()
  await expect(
    panel.getByRole('heading', { name: 'Kanso Stone Basin' }),
  ).toBeVisible()
  await openDemoReset(page)
  await page
    .getByRole('dialog', {
      name: 'Reset catalog and Home demo for Atelier Kyoto?',
    })
    .getByRole('button', { name: 'Confirm demo reset' })
    .click()
  await expect(
    page.getByRole('status').filter({ hasText: 'Catalog and Home demo reset' }),
  ).toBeVisible()
  await expect(card(page, 'Needs attention')).toContainText('4')
  await expect(
    page.getByRole('button', { name: 'View details for Kanso Stone Basin' }),
  ).toBeVisible()
})

test('namespace switching and navigation preserve demo changes; reload restores fixtures', async ({
  page,
}, testInfo) => {
  await page.goto(home)
  await page
    .getByRole('button', { name: 'View details for Ceremonial Tea Ritual Kit' })
    .click()
  await page.getByRole('button', { name: 'Refresh demo status' }).click()
  await page.getByRole('button', { name: 'Confirm demo change' }).click()
  await page
    .getByRole('button', { name: 'Close inspector', exact: true })
    .click()
  await page.reload()
  await expect(card(page, 'Needs attention')).toContainText('4')
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Studio Berlin/ }).click()
  await expect(
    page.getByRole('heading', { name: 'Your catalog starts here' }),
  ).toBeVisible()
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await expect(
    page.getByRole('link', { name: 'Open setup guide', exact: true }),
  ).toHaveAttribute('href', '/w/studio-berlin/home/setup-guide')
  await page.screenshot({
    path: testInfo.outputPath('overview-first-use.png'),
    fullPage: true,
  })
  await page
    .getByRole('link', { name: 'Open setup guide', exact: true })
    .click()
  await expect(
    page.getByRole('heading', { name: 'Setup guide', exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('progressbar', { name: 'Demo setup progress' }),
  ).toHaveAttribute('value', '0')
  await page.goBack()
  await expect(
    page.getByRole('heading', { name: 'Your catalog starts here' }),
  ).toBeVisible()
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Atelier Kyoto/ }).click()
  await expect(card(page, 'Needs attention')).toContainText('4')
  await page
    .getByRole('button', { name: 'View details for Kanso Stone Basin' })
    .click()
  await page.getByRole('button', { name: 'Apply demo fix' }).click()
  await page.getByRole('button', { name: 'Confirm demo change' }).click()
  await page
    .getByRole('button', { name: 'Close inspector', exact: true })
    .click()
  await page
    .getByRole('navigation', { name: 'Main navigation', exact: true })
    .getByRole('link', { name: 'My work', exact: true })
    .click()
  await page.goBack()
  await expect(card(page, 'Needs attention')).toContainText('3')
})

for (const width of [1440, 900, 390]) {
  test(`Overview is accessible and fits at ${width}px, with an inspector and local confirmation`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto(home)
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
    await page.screenshot({
      path: testInfo.outputPath(`overview-${width}-light.png`),
      fullPage: true,
    })
    if (width === 1440) await page.getByText('Dark', { exact: true }).click()
    else
      await page.evaluate(() => localStorage.setItem('gitstore.theme', 'dark'))
    if (width !== 1440) await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.screenshot({
      path: testInfo.outputPath(`overview-${width}-dark.png`),
      fullPage: true,
    })
    await page
      .getByRole('button', { name: 'View details for Kanso Stone Basin' })
      .click()
    const panel =
      width > 1100
        ? page.getByRole('complementary', { name: 'Page context' })
        : page.getByRole('dialog', { name: 'Page context', exact: true })
    await expect(
      panel.getByRole('heading', { name: 'Kanso Stone Basin' }),
    ).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.screenshot({
      path: testInfo.outputPath(`overview-${width}-inspector.png`),
      fullPage: true,
    })
    await panel.getByRole('button', { name: 'Apply demo fix' }).click()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.getByRole('button', { name: 'Confirm demo change' }).click()
    await expect(
      panel.getByRole('heading', { name: 'Kanso Stone Basin' }),
    ).toBeFocused()
    await page
      .getByRole('button', { name: 'Close inspector', exact: true })
      .click()
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
  })
}

test('Focus Mode preserves local Overview changes', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto(home)
  await page
    .getByRole('button', { name: 'View details for Kanso Stone Basin' })
    .click()
  await page.getByRole('button', { name: 'Apply demo fix' }).click()
  await page.getByRole('button', { name: 'Confirm demo change' }).click()
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await expect(card(page, 'Needs attention')).toContainText('3')
  await page.getByRole('button', { name: 'Exit Focus Mode' }).click()
  await expect(card(page, 'Needs attention')).toContainText('3')
  await page.getByRole('button', { name: 'Inspector', exact: true }).click()
  await expect(
    page.getByRole('complementary', { name: 'Page context' }),
  ).toContainText('No demo action needed')
})

test('Storybook covers all-clear, filtered-empty, loading, unavailable, stale and restricted states', async ({
  page,
}) => {
  const states = [
    { id: 'all-clear', text: 'Nothing needs attention.' },
    { id: 'filtered-empty', text: 'No matching items.' },
    { id: 'loading', text: 'Loading demo overview' },
    { id: 'unavailable', text: 'Demo data unavailable' },
    { id: 'stale', text: 'Demo information is out of date.' },
    { id: 'restricted', text: 'No access in this preview' },
  ]
  for (const state of states) {
    await page.goto(
      `${storybookURL}/iframe.html?id=pages-home-overview--${state.id}&viewMode=story`,
    )
    await expect(page.getByText(state.text, { exact: true })).toBeVisible()
    if (state.id === 'filtered-empty') {
      await page
        .getByRole('button', { name: 'Clear filters', exact: true })
        .first()
        .click()
      await expect(
        page.getByRole('button', { name: /^View details for / }),
      ).toHaveCount(5)
    }
    if (state.id === 'unavailable') {
      await page.getByRole('button', { name: 'Reload demo' }).click()
      await expect(
        page.getByRole('region', { name: 'Store summary' }),
      ).toBeVisible()
    }
  }
})
