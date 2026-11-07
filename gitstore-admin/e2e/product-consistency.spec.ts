import { storybookURL } from './storybook-url'
import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
const root = '/w/atelier-kyoto'
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
})
for (const width of [375, 912, 1508])
  test(`Products follows shared heading, demo alignment and filters at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.goto(`${root}/home/recent-activity`)
    const demo = page.getByRole('button', { name: 'Demo data', exact: true })
    const activityBox = (await demo.boundingBox())!
    const activityFields = page.locator('[data-desktop-filters]')
    const fieldBox = width > 760 ? await activityFields.boundingBox() : null
    await page.goto(`${root}/catalog/products`)
    const productBox = (await demo.boundingBox())!
    expect(
      Math.abs(
        productBox.x + productBox.width - activityBox.x - activityBox.width,
      ),
    ).toBeLessThanOrEqual(2)
    expect(Math.abs(productBox.y - activityBox.y)).toBeLessThanOrEqual(2)
    if (width <= 760) {
      await expect(page.locator('[data-page-context]')).toBeHidden()
      await expect(page.locator('[data-desktop-filters]')).toBeHidden()
      await expect(
        page.getByRole('button', { name: 'Filters', exact: true }),
      ).toBeVisible()
    } else {
      const fields = page.locator('[data-desktop-filters]')
      await expect(fields).toBeVisible()
      expect(
        Math.abs((await fields.boundingBox())!.width - fieldBox!.width),
      ).toBeLessThanOrEqual(2)
      const buttons = fields.getByRole('button')
      const boxes = await buttons.evaluateAll((nodes) =>
        nodes.map((node) => {
          const r = node.getBoundingClientRect()
          return { y: r.y, height: r.height }
        }),
      )
      expect(new Set(boxes.map((b) => b.y)).size).toBe(1)
      const search = (await page
        .getByRole('searchbox', { name: 'Search products' })
        .boundingBox())!
      if (width === 1508)
        expect(Math.abs(search.y - boxes[0]!.y)).toBeLessThanOrEqual(12)
    }
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  })
test('mobile Product filters stage changes, cancel, apply counts and restore focus', async ({
  page,
}) => {
  await page.setViewportSize({ width: 375, height: 667 })
  await page.goto(`${root}/catalog/products`)
  const trigger = page.getByRole('button', { name: /^Filters/ })
  await trigger.click()
  const sheet = page.getByRole('dialog', {
    name: 'Filter products',
    exact: true,
  })
  await sheet.getByRole('button', { name: /Status$/ }).click()
  await page.getByRole('option', { name: 'Ready', exact: true }).click()
  await expect(
    sheet.getByRole('button', { name: 'Show 2 products' }),
  ).toBeVisible()
  await sheet.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(trigger).toBeFocused()
  await expect(page.getByRole('link', { name: /^View / })).toHaveCount(4)
  await trigger.click()
  await sheet.getByRole('button', { name: /Status$/ }).click()
  await page.getByRole('option', { name: 'Ready', exact: true }).click()
  await sheet.getByRole('button', { name: 'Show 2 products' }).click()
  await expect(trigger).toBeFocused()
  await expect(page).toHaveURL(/status=ready/)
  await expect(page.getByRole('link', { name: /^View / })).toHaveCount(2)
  await page
    .getByRole('button', { name: 'Remove status filter: Ready' })
    .click()
  await expect(page.getByRole('link', { name: /^View / })).toHaveCount(4)
})
for (const pagePath of ['stone-basin', 'stone-basin/edit', 'new'])
  test(`Product ${pagePath} keeps Demo data right-aligned and hides mobile context`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 375, height: 667 })
    await page.goto(`${root}/catalog/products/${pagePath}`)
    await expect(page.locator('[data-page-context]')).toBeHidden()
    const box = (await page
      .getByRole('button', { name: 'Demo data', exact: true })
      .boundingBox())!
    expect(375 - box.x - box.width).toBeCloseTo(16, 0)
  })
test('Product tags create chips, deduplicate, remove by keyboard, and save after blur', async ({
  page,
}) => {
  await page.setViewportSize({ width: 375, height: 667 })
  await page.goto(`${root}/catalog/products/new`)
  await page
    .getByRole('textbox', { name: 'Title', exact: true })
    .fill('A tagged cup')
  const input = page.getByRole('textbox', { name: 'Add tag', exact: true })
  await input.fill('ceramics')
  await input.press('Enter')
  await input.fill('homeware')
  await input.press(',')
  await input.fill('ceramics')
  await input.press('Enter')
  await expect(
    page.getByRole('button', { name: 'Remove tag ceramics' }),
  ).toHaveCount(1)
  await input.press('Backspace')
  await page.keyboard.press('Delete')
  await expect(
    page.getByRole('button', { name: 'Remove tag homeware' }),
  ).toHaveCount(0)
  await input.fill('handmade')
  await page.getByRole('button', { name: 'Create demo product' }).click()
  await expect(
    page.getByText('ceramics, handmade', { exact: true }),
  ).toBeVisible()
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
})

test('tag chip Storybook interaction completes', async ({ page }) => {
  await page.setViewportSize({ width: 981, height: 763 })
  await page.goto(`${storybookURL}/?path=/story/foundation-tag-input--keyboard`)
  await page.getByRole('tab', { name: /^Interactions/ }).click()
  await expect(
    page
      .getByRole('toolbar', { name: 'Component test playback controls' })
      .getByText(/^pass$/i),
  ).toBeVisible({ timeout: 20000 })
})
