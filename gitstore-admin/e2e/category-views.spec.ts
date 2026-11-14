import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { storybookURL } from './storybook-url'
const root = '/w/atelier-kyoto/catalog/categories'
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
})

test('columns drill down without opening a detail, preserve views, and keep both context trails', async ({
  page,
}, testInfo) => {
  await page.goto(root)
  await expect(
    page.getByRole('button', { name: 'Columns view' }),
  ).toHaveAttribute('aria-pressed', 'true')
  await expect(
    page
      .getByRole('navigation', { name: 'Breadcrumb', exact: true })
      .locator('ol'),
  ).toHaveText('CatalogCategories')
  await expect(page.locator('main [data-page-context]')).toContainText(
    'Atelier Kyoto',
  )
  await expect(page.locator('main [data-page-context]')).toContainText(
    'Catalog',
  )
  for (const title of ['Homeware', 'Ceramics', 'Tableware']) {
    const button = page.getByRole('button', {
      name: `Explore ${title}`,
      exact: true,
    })
    await button.focus()
    await button.press('Enter')
  }
  await expect(page).toHaveURL(/selected=tableware/)
  await expect(page).not.toHaveURL(/categories\/tableware/)
  await expect(page.locator('[data-category-columns] > section')).toHaveCount(3)
  const path = page.getByRole('navigation', {
    name: 'Category path',
    exact: true,
  })
  await expect(path.getByRole('link')).toHaveText(['Homeware', 'Ceramics'])
  await expect(path.locator('[aria-current="page"]')).toHaveText('Tableware')
  await page.mouse.click(1, 1)
  await page.evaluate(() => document.fonts.ready)
  await page.screenshot({ path: testInfo.outputPath('readme-categories.png') })
  await page.getByRole('button', { name: 'Cards view', exact: true }).click()
  await expect(page).toHaveURL(/selected=tableware/)
  const card = page.locator('a[data-resource-id="tableware"]')
  await expect(card.getByRole('link')).toHaveCount(0)
  await expect(card).not.toContainText('View category')
  await card.click()
  await expect(page.locator('main h1')).toHaveText('Tableware')
  await expect(
    page
      .getByRole('navigation', { name: 'Breadcrumb', exact: true })
      .getByRole('link'),
  ).toHaveText(['Catalog', 'Categories'])
  await expect(page.locator('main [data-product-image] img')).toBeVisible()
  await page.goBack()
  await expect(
    page.getByRole('button', { name: 'Cards view' }),
  ).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('button', { name: 'Columns view' }).click()
  await expect(
    page.getByRole('region', { name: 'Selected category' }),
  ).toContainText('Tableware')
  await page.reload()
  await expect(
    page.getByRole('region', { name: 'Selected category' }),
  ).toBeVisible()
})

test('tree and cards retain matching ancestors, filters and direct detail actions', async ({
  page,
}) => {
  await page.goto(`${root}?view=tree&q=Tableware`)
  await expect(page.getByRole('status')).toContainText('1 categories shown')
  await expect(page.locator('a[data-resource-id]')).toHaveCount(3)
  await expect(page.locator('[data-context-only]')).toHaveCount(2)
  await expect(
    page.getByRole('button', { name: 'Collapse all' }),
  ).toBeDisabled()
  await expect(page.getByRole('button', { name: /View category/ })).toHaveCount(
    0,
  )
  await page.getByRole('button', { name: 'Cards view' }).click()
  await expect(page.locator('a[data-resource-id]')).toHaveCount(1)
  await expect(page.getByRole('searchbox')).toHaveValue('Tableware')
  await page.getByRole('button', { name: 'Clear filters', exact: true }).click()
  await expect(page.locator('a[data-resource-id]')).toHaveCount(4)
  await page.getByRole('button', { name: 'Tree view' }).click()
  await page
    .getByRole('button', { name: 'Collapse Homeware', exact: true })
    .click()
  await expect(page.locator('a[data-resource-id="ceramics"]')).toHaveCount(0)
  await page.getByRole('button', { name: 'Expand all', exact: true }).click()
  await expect(page.locator('a[data-resource-id="tableware"]')).toBeVisible()
  await page.locator('a[data-resource-id="tableware"]').click()
  await expect(page.locator('main h1')).toHaveText('Tableware')
})

test('mobile columns are one-level navigation with a parent action and a visible detail action', async ({
  page,
}) => {
  await page.setViewportSize({ width: 375, height: 667 })
  await page.goto(root)
  await expect(
    page.getByRole('navigation', { name: 'Breadcrumb', exact: true }),
  ).toBeHidden()
  await expect(page.locator('main [data-page-context]')).toBeHidden()
  await page
    .getByRole('button', { name: 'Explore Homeware', exact: true })
    .click()
  await expect(page.locator('[data-category-columns] > section')).toHaveCount(1)
  await expect(page.locator('[data-category-columns] h2')).toHaveText(
    'Homeware',
  )
  await page
    .getByRole('button', { name: 'Explore Ceramics', exact: true })
    .click()
  await expect(page.locator('[data-category-columns] h2')).toHaveText(
    'Ceramics',
  )
  await page.getByRole('button', { name: 'Homeware', exact: true }).click()
  await expect(page.locator('[data-category-columns] h2')).toHaveText(
    'Homeware',
  )
  await page.goBack()
  await expect(page.locator('[data-category-columns] h2')).toHaveText(
    'Ceramics',
  )
  await page
    .getByRole('button', { name: 'Explore Tableware', exact: true })
    .click()
  await page.getByRole('link', { name: /View category/ }).click()
  await expect(page.locator('main h1')).toHaveText('Tableware')
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true)
})

test('deep paths, missing category images and selection survive responsive changes without backend requests', async ({
  page,
}) => {
  const backend: string[] = []
  page.on('request', (request) => {
    if (/graphql|\/api\//.test(request.url())) backend.push(request.url())
  })
  await page.goto(
    `${storybookURL}/iframe.html?id=pages-catalog-categories--deep-columns&viewMode=story`,
  )
  await expect(
    page.getByRole('region', { name: 'Selected category' }),
  ).toContainText('Category level 12')
  await expect(page.locator('[data-category-columns] > section')).toHaveCount(3)
  await expect(
    page.getByRole('button', { name: 'Show omitted category ancestors' }),
  ).toBeVisible()
  await page.setViewportSize({ width: 892, height: 900 })
  await expect(page.locator('[data-category-columns] > section')).toHaveCount(2)
  await page.setViewportSize({ width: 375, height: 667 })
  await expect(page.locator('[data-category-columns] > section')).toHaveCount(1)
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true)
  await page.goto(
    `${storybookURL}/iframe.html?id=pages-catalog-categories--missing-media&viewMode=story`,
  )
  await expect(page.locator('a[data-resource-id="ceramics"] img')).toHaveCount(
    0,
  )
  await expect(page.getByText('No category image').first()).toBeVisible()
  expect(backend).toEqual([])
})

for (const width of [1440, 892, 375])
  for (const theme of ['light', 'dark'] as const)
    test(`all category views retain layout, breadcrumbs and contrast at ${width}px in ${theme}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 900 })
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      for (const view of ['columns', 'tree', 'cards']) {
        await page.goto(`${root}?view=${view}&selected=tableware`)
        await expect(
          page.locator(`[data-category-browser="${view}"]`),
        ).toBeVisible()
        await expect(
          page.getByRole('button', { name: 'Columns view' }),
        ).toBeVisible()
        await expect(
          page.getByRole('button', { name: 'Tree view' }),
        ).toBeVisible()
        await expect(
          page.getByRole('button', { name: 'Cards view' }),
        ).toBeVisible()
        expect((await new AxeBuilder({ page }).analyze()).violations).toEqual(
          [],
        )
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        ).toBe(true)
        await page.screenshot({
          path: testInfo.outputPath(`categories-${view}-${width}-${theme}.png`),
        })
      }
    })
