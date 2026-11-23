import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { storybookURL } from './storybook-url'
import { openDemoReset } from './home-interactions'
const root = '/w/atelier-kyoto/catalog',
  attributes = `${root}/attributes-options`,
  types = `${root}/product-types`
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
})
test('saved options have Product-scoped detail identity, actual choices, Variant links and consistent trails', async ({
  page,
}) => {
  const requests: string[] = []
  page.on('request', (r) => {
    if (/graphql|\/api\//.test(r.url())) requests.push(r.url())
  })
  await page.goto(attributes)
  await expect(page).toHaveTitle('Attributes & options · GitStore')
  await expect(
    page.getByRole('tab', { name: /Product options 2/ }),
  ).toHaveAttribute('aria-selected', 'true')
  const option = page.locator('[data-resource-id="option:silk-tunic:color"]')
  await option.focus()
  await option.press('Enter')
  await expect(page.locator('main h1')).toHaveText('Color')
  await expect(page.locator('main h1')).toBeFocused()
  await expect(page).toHaveTitle('Color · GitStore')
  await expect(
    page
      .getByRole('navigation', { name: 'Breadcrumb', exact: true })
      .getByRole('link'),
  ).toHaveText(['Catalog', 'Attributes & options'])
  await expect(
    page.getByRole('heading', { name: 'Offered values' }),
  ).toBeVisible()
  await expect(page.locator('main')).toContainText('Natural')
  await expect(page.locator('main')).toContainText('Black')
  await expect(
    page.getByRole('link', { name: 'Raw Silk Tunic — Black / M', exact: true }),
  ).toBeVisible()
  await page.getByText('Technical details', { exact: true }).click()
  await expect(
    page.getByText('catalog/products/raw-silk-tunic.md', { exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('complementary', { name: 'Design preview' }),
  ).toHaveCount(0)
  await page.reload()
  await expect(page.locator('main h1')).toHaveText('Color')
  await page.goBack()
  await expect(option).toBeFocused()
  await page.goForward()
  await expect(page.locator('main h1')).toHaveText('Color')
  await page.goto(`${attributes}/products/silk-tunic/options/missing`)
  await expect(
    page.getByRole('heading', { name: 'Option or definition unavailable' }),
  ).toBeVisible()
  expect(requests).toEqual([])
})
test('definition tab and type examples stay separate from saved Products with direct-link return context', async ({
  page,
}) => {
  await page.goto(attributes)
  await page.getByRole('tab', { name: /Reusable definitions 3/ }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(3)
  await expect(
    page.getByRole('complementary', { name: 'Design preview' }),
  ).toBeVisible()
  await page.locator('[data-resource-id="definition:color"]').click()
  await expect(page.locator('main h1')).toHaveText('Color')
  await expect(
    page.getByRole('heading', { name: 'Example choices' }),
  ).toBeVisible()
  await page.getByRole('link', { name: 'Apparel', exact: true }).click()
  await expect(page.locator('main h1')).toHaveText('Apparel')
  await expect(page).toHaveTitle('Apparel · GitStore')
  await expect(
    page.getByRole('heading', { name: 'Example Products' }),
  ).toBeVisible()
  await page.getByRole('link', { name: 'Color', exact: true }).click()
  await expect(
    page.getByRole('heading', { name: 'Example choices' }),
  ).toBeVisible()
  await page
    .getByRole('link', { name: 'Attributes & options', exact: true })
    .last()
    .click()
  await expect(
    page.getByRole('tab', { name: /Reusable definitions/ }),
  ).toHaveAttribute('aria-selected', 'true')
  await page.goto(`${attributes}/definitions/material`)
  await page
    .getByRole('navigation', { name: 'Breadcrumb', exact: true })
    .getByRole('link', { name: 'Attributes & options' })
    .click()
  await expect(page).toHaveURL(/tab=definitions/)
  await expect(
    page.getByRole('tab', { name: /Reusable definitions/ }),
  ).toHaveAttribute('aria-selected', 'true')
  await page.goto(`${types}/homeware`)
  await page
    .getByRole('link', { name: 'Kanso Stone Basin', exact: true })
    .click()
  await expect(page.locator('main h1')).toHaveText('Kanso Stone Basin')
  await expect(page.locator('main')).not.toContainText('Homeware type')
  await expect(
    page.getByRole('button', {
      name: /^(Create definition|Assign type|Delete type|Save definition)/,
    }),
  ).toHaveCount(0)
})
test('query filters, counted tabs, display preferences, mobile filter staging and store isolation work', async ({
  page,
}) => {
  await page.goto(attributes)
  await page.getByRole('button', { name: 'Cards view' }).click()
  await page.getByRole('searchbox').fill('unmatched')
  await expect(
    page.getByRole('heading', { name: 'No matching product options' }),
  ).toBeVisible()
  await expect(
    page.getByRole('tab', { name: /Product options 2/ }),
  ).toBeVisible()
  await page.reload()
  await expect(page.getByRole('searchbox')).toHaveValue('unmatched')
  await page.getByRole('tab', { name: /Reusable definitions/ }).click()
  await expect(page.getByRole('searchbox')).toHaveValue('')
  await expect(page.locator('[data-resource-id]')).toHaveCount(3)
  await page.goto(types)
  await page.getByRole('button', { name: /Definition example$/ }).click()
  await page.getByRole('option', { name: 'Material', exact: true }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(1)
  await expect(page.locator('[data-resource-id="type:homeware"]')).toBeVisible()
  await page.setViewportSize({ width: 375, height: 667 })
  await page.getByRole('button', { name: /^Filters/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Filter product types' })
  await sheet.getByRole('button', { name: /Definition example$/ }).click()
  await page.getByRole('option', { name: 'Color', exact: true }).click()
  await sheet.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(page).toHaveURL(/related=material/)
  await page.getByRole('button', { name: /^Filters/ }).click()
  await sheet.getByRole('button', { name: /Definition example$/ }).click()
  await page.getByRole('option', { name: 'Color', exact: true }).click()
  await sheet.getByRole('button', { name: 'Apply filters' }).click()
  await expect(page.locator('[data-resource-id="type:apparel"]')).toBeVisible()
  await page.goto('/w/studio-berlin/catalog/product-types')
  await expect(
    page.getByRole('heading', { name: 'No Product type examples' }),
  ).toBeVisible()
  await page.goto('/w/studio-berlin/catalog/attributes-options')
  await expect(
    page.getByRole('heading', { name: 'No Product options yet' }),
  ).toBeVisible()
  await page.goto(attributes)
  await expect(
    page.getByRole('button', { name: 'Cards view' }),
  ).toHaveAttribute('aria-pressed', 'true')
})
test('Product option edits invalidate renamed deep links and reset restores options without changing definitions', async ({
  page,
}) => {
  await page.goto(`${attributes}/products/silk-tunic/options/color`)
  await page.getByRole('link', { name: 'Raw Silk Tunic', exact: true }).click()
  await page.getByRole('link', { name: 'Edit product', exact: true }).click()
  await page
    .getByRole('textbox', { name: 'Option name', exact: true })
    .nth(1)
    .fill('finish')
  await page
    .getByRole('textbox', { name: 'Option Title', exact: true })
    .nth(1)
    .fill('Finish')
  await page
    .getByRole('button', { name: 'Save demo changes', exact: true })
    .click()
  await expect(page).toHaveURL(/products\/silk-tunic(?:\?|$)/)
  await page
    .getByRole('link', { name: 'Attributes & options', exact: true })
    .click()
  await expect(
    page.locator('[data-resource-id="option:silk-tunic:finish"]'),
  ).toBeVisible()
  await expect(
    page.locator('[data-resource-id="option:silk-tunic:color"]'),
  ).toHaveCount(0)
  await page.goBack()
  await expect(page).toHaveURL(/products\/silk-tunic(?:\?|$)/)
  await page.goBack()
  await expect(page).toHaveURL(/products\/silk-tunic\/edit(?:\?|$)/)
  await page.goBack()
  await expect(page).toHaveURL(/products\/silk-tunic(?:\?|$)/)
  await page.goBack()
  await expect(
    page.getByRole('heading', { name: 'Option or definition unavailable' }),
  ).toBeVisible()
  await page
    .getByRole('link', { name: 'Attributes & options', exact: true })
    .last()
    .click()
  await openDemoReset(page)
  await page.getByRole('button', { name: 'Confirm demo reset' }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(2)
  await page.getByRole('tab', { name: /Reusable definitions/ }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(3)
})
test('bounded example batches and selected-row focus survive detail return', async ({
  page,
}) => {
  await page.goto(
    `${storybookURL}/iframe.html?id=pages-catalog-product-types--more-results&viewMode=story`,
  )
  await expect(page.locator('[data-resource-id]')).toHaveCount(6)
  await page.getByRole('button', { name: 'Load more', exact: true }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(12)
  const card = page.locator('[data-resource-id]').nth(11),
    title = await card.locator('[data-product-title]').innerText()
  await card.focus()
  await card.press('Enter')
  await expect(page.locator('main h1')).toHaveText(title)
  await page
    .getByRole('link', { name: 'Product types', exact: true })
    .last()
    .click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(12)
  await expect(card).toBeFocused()
  await page.getByRole('button', { name: 'Load more', exact: true }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(15)
  await expect(
    page.getByRole('button', { name: 'Load more', exact: true }),
  ).toHaveCount(0)
})
for (const [story, expected] of [
  [
    'pages-catalog-product-types--missing-relationships',
    'Definition example unavailable',
  ],
  [
    'pages-catalog-attributes-options--unmatched-selection',
    'This value is not offered by the Product.',
  ],
  [
    'pages-catalog-attributes-options--missing-option',
    'Option or definition unavailable',
  ],
  ['pages-catalog-product-types--loading', 'Loading product types'],
  ['pages-catalog-product-types--unavailable', 'Product types unavailable'],
] as const)
  test(`${story} exposes honest read-only states`, async ({ page }) => {
    await page.goto(`${storybookURL}/iframe.html?id=${story}&viewMode=story`)
    await expect(page.locator('main')).toContainText(expected)
    await expect(
      page.getByRole('button', {
        name: /^(Create|Save|Delete|Assign|Publish)/,
      }),
    ).toHaveCount(0)
    if (story.includes('missing-relationships')) {
      await expect(
        page.getByText('Example Product unavailable', { exact: true }),
      ).toBeVisible()
      await expect(page.locator('main a[href*=missing]')).toHaveCount(0)
    }
  })
for (const width of [1440, 892, 375])
  for (const theme of ['light', 'dark'] as const)
    test(`catalog structure fits ${width}px in ${theme} with accessible detail and Focus state`, async ({
      page,
    }, info) => {
      await page.setViewportSize({ width, height: 900 })
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      for (const path of [
        attributes,
        `${attributes}?tab=definitions&view=grid`,
        `${types}?view=grid`,
      ]) {
        await page.goto(path)
        await expect(page.locator('[data-structure-list]')).toBeVisible()
        if (path.startsWith(attributes)) {
          await expect(
            page.locator('main header').getByRole('tablist'),
          ).toBeVisible()
          await page.screenshot({
            path: info.outputPath(
              `attributes-${path.includes('definitions') ? 'definitions' : 'options'}-${width}-${theme}.png`,
            ),
            fullPage: true,
          })
        }
        if (path.includes('view=grid')) {
          await expect(
            page
              .locator('[data-catalog-card]')
              .first()
              .locator('[data-product-image] [data-media-preview]'),
          ).toBeVisible()
        }
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        ).toBe(true)
        expect((await new AxeBuilder({ page }).analyze()).violations).toEqual(
          [],
        )
      }
      await page.screenshot({
        path: info.outputPath(`product-types-${width}-${theme}.png`),
        fullPage: true,
      })
      await page.locator('[data-resource-id="type:apparel"]').click()
      await expect(page.locator('main h1')).toHaveText('Apparel')
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true)
      await page.screenshot({
        path: info.outputPath(`product-type-detail-${width}-${theme}.png`),
        fullPage: true,
      })
      await page
        .getByRole('button', { name: 'Focus mode', exact: true })
        .click()
      await expect(page.locator('main h1')).toHaveText('Apparel')
      await page
        .getByRole('button', { name: 'Exit Focus Mode', exact: true })
        .click()
      await page.goto(`${attributes}/products/silk-tunic/options/color`)
      await expect(page.locator('main h1')).toHaveText('Color')
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
      await page.screenshot({
        path: info.outputPath(`product-option-detail-${width}-${theme}.png`),
        fullPage: true,
      })
    })
for (const story of [
  'pages-catalog-attributes-options--definition-navigation',
  'pages-catalog-product-types--relationship-navigation',
])
  test(`${story} completes its Storybook interaction`, async ({ page }) => {
    await page.goto(`${storybookURL}/?path=/story/${story}`)
    await page.getByRole('tab', { name: /^Interactions/ }).click()
    await expect(
      page
        .getByRole('toolbar', { name: 'Component test playback controls' })
        .getByText(/^pass$/i),
    ).toBeVisible({ timeout: 20000 })
  })

test('hover preloading keeps definition/type round trips inside the client router', async ({
  page,
}) => {
  await page.goto(`${types}/apparel`)
  await expect(page.locator('main h1')).toHaveText('Apparel')
  const documents: string[] = []
  page.on('request', (r) => {
    if (r.isNavigationRequest()) documents.push(r.url())
  })
  const color = page.getByRole('link', { name: 'Color', exact: true })
  await color.hover()
  await page.waitForTimeout(200)
  await color.click()
  await expect(
    page.getByRole('heading', { name: 'Example choices' }),
  ).toBeVisible()
  const apparel = page.getByRole('link', { name: 'Apparel', exact: true })
  await apparel.hover()
  await page.waitForTimeout(200)
  await apparel.click()
  await expect(page.locator('main h1')).toHaveText('Apparel')
  expect(documents).toEqual([])
})
