import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { storybookURL } from './storybook-url'

for (const theme of ['light', 'dark'] as const) {
  test(`small Home screens show useful content and consistent empty states in ${theme}`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 375, height: 667 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    for (const [destination, heading] of [
      ['overview', 'Your catalog starts here'],
      ['my-work', 'No work yet'],
      ['recent-activity', 'No activity yet'],
    ]) {
      await page.goto(`/w/studio-berlin/home/${destination}`)
      const empty = page.locator('[data-home-empty-state]')
      await expect(
        empty.getByRole('heading', { name: heading, exact: true }),
      ).toBeVisible()
      const bounds = (await empty.boundingBox())!
      expect(bounds.y + bounds.height).toBeLessThan(600)
      await expect(page.getByRole('searchbox')).toHaveCount(0)
      await expect(page.locator('[data-halo-card]')).toHaveCount(0)
      await page.screenshot({
        path: testInfo.outputPath(`empty-${destination}-${theme}.png`),
      })
    }
    await page.goto('/w/atelier-kyoto/home/recent-activity')
    const entry = page.locator('[data-resource-id]').first()
    await expect(entry).toBeVisible()
    const bounds = (await entry.boundingBox())!
    expect(bounds.y + bounds.height).toBeLessThan(600)
    await expect(
      page.getByRole('button', { name: /Recorded date$/ }),
    ).toBeHidden()
    await page.getByRole('button', { name: 'Demo data', exact: true }).click()
    await expect(
      page.getByRole('dialog', { name: 'About this demo' }),
    ).toContainText('Reloading resets both stores')
    await expect(
      page.getByRole('dialog', { name: 'About this demo' }),
    ).toContainText('2 Nov 2026')
    await page.keyboard.press('Escape')
    await expect(
      page.getByRole('button', { name: 'Demo data', exact: true }),
    ).toBeFocused()
    await page.getByRole('button', { name: 'Demo data', exact: true }).click()
    await page.getByRole('button', { name: 'Reset demo', exact: true }).click()
    const confirmation = page.getByRole('dialog', {
      name: 'Reset catalog and Home demo for Atelier Kyoto?',
    })
    await expect(confirmation).toBeVisible()
    await expect(
      page.getByRole('dialog', { name: 'About this demo' }),
    ).toHaveCount(0)
    await confirmation
      .getByRole('button', { name: 'Cancel', exact: true })
      .click()
    await expect(
      page.getByRole('button', { name: 'Demo data', exact: true }),
    ).toBeFocused()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
    await page.screenshot({
      path: testInfo.outputPath(`activity-mobile-${theme}.png`),
    })
  })
}

test('mobile filter sheet stages changes, applies, removes chips, cancels, and restores focus', async ({
  page,
}) => {
  await page.setViewportSize({ width: 375, height: 667 })
  await page.goto('/w/atelier-kyoto/home/recent-activity')
  const button = page.getByRole('button', { name: /^Filters/ })
  await button.click()
  const sheet = page.getByRole('dialog', { name: 'Filter activity' })
  await sheet.getByRole('button', { name: /Recorded date$/ }).click()
  await page.getByRole('option', { name: '31 Oct 2026', exact: true }).click()
  await sheet.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(button).toBeFocused()
  await expect(button).toHaveText('Filters')
  await button.click()
  await sheet.getByRole('button', { name: /Recorded date$/ }).click()
  await page.getByRole('option', { name: '31 Oct 2026', exact: true }).click()
  await sheet.getByRole('button', { name: 'Apply filters' }).click()
  await expect(button).toHaveText('Filters (1)')
  await expect(button).toBeFocused()
  await expect(
    page.getByRole('heading', { name: '31 Oct 2026', exact: true }),
  ).toBeVisible()
  await page
    .getByRole('button', { name: 'Remove date filter: 31 Oct 2026' })
    .click()
  await expect(button).toHaveText('Filters')
  await page
    .getByRole('searchbox', { name: 'Search activity' })
    .fill('not an item')
  await expect(
    page.getByRole('heading', { name: 'No matching activity', exact: true }),
  ).toBeVisible()
  await page
    .getByRole('button', { name: 'Clear filters', exact: true })
    .last()
    .click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(10)
})

for (const width of [981, 1440]) {
  test(`Storybook profile menu adapts to the actual canvas at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 763 })
    await page.goto(
      `${storybookURL}/?path=/story/foundation-layouts--profile-menu-open`,
    )
    const canvas = page.frameLocator('#storybook-preview-iframe')
    await expect(
      canvas.getByRole('menu').filter({
        has: canvas.getByRole('menuitem', {
          name: 'Profile & preferences',
          exact: true,
        }),
      }),
    ).toBeVisible({ timeout: 15_000 })
    await page.getByRole('tab', { name: /^Interactions\b/ }).click()
    await expect(
      page
        .getByRole('toolbar', { name: 'Component test playback controls' })
        .getByText(/^pass$/i),
    ).toBeVisible()
  })
}

test('expanded disclosure specimens prove their state and native browser keyboard toggles the disclosure', async ({
  page,
}) => {
  await page.setViewportSize({ width: 981, height: 763 })
  for (const story of ['expanded', 'long-path']) {
    await page.goto(
      `${storybookURL}/?path=/story/foundation-technical-details--${story}`,
    )
    const canvas = page.frameLocator('#storybook-preview-iframe')
    await expect(canvas.locator('details')).toHaveJSProperty('open', true)
    await expect(canvas.locator('details code')).toBeVisible()
    await page.getByRole('tab', { name: /^Interactions\b/ }).click()
    await expect(
      page
        .getByRole('toolbar', { name: 'Component test playback controls' })
        .getByText(/^pass$/i),
    ).toBeVisible()
  }
  await page.goto(
    `${storybookURL}/iframe.html?id=foundation-technical-details--collapsed&viewMode=story`,
  )
  const summary = page.locator('summary')
  await expect(page.locator('details')).toHaveJSProperty('open', false)
  await summary.focus()
  await page.keyboard.press('Enter')
  await expect(page.locator('details')).toHaveJSProperty('open', true)
  await expect(page.locator('details code')).toBeVisible()
  await page.keyboard.press('Space')
  await expect(page.locator('details')).toHaveJSProperty('open', false)
  await expect(summary).toBeFocused()
})
