import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
const root = '/w/atelier-kyoto'
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
})
test('empty health cells are plain values while populated resource counts navigate', async ({
  page,
}) => {
  await page.goto(`${root}/home/overview`)
  const cells = page.locator('table tbody td')
  for (const cell of await cells.all()) {
    if (['—', '0'].includes((await cell.innerText()).trim()))
      await expect(cell.getByRole('link')).toHaveCount(0)
  }
  await expect(
    page.getByRole('link', { name: '4 Ready categories', exact: true }),
  ).toBeVisible()
})
test('catalog display preferences survive navigation, reload and store switches without persisting demo records', async ({
  page,
}) => {
  for (const domain of ['products', 'variants', 'collections']) {
    await page.goto(`${root}/catalog/${domain}`)
    await page.getByRole('button', { name: 'Grid view', exact: true }).click()
    await page.goto(`${root}/home/overview`)
    await page.goto(`${root}/catalog/${domain}`)
    await expect(
      page.getByRole('button', { name: 'Grid view', exact: true }),
    ).toHaveAttribute('aria-pressed', 'true')
    await page.reload()
    await expect(
      page.getByRole('button', { name: 'Grid view', exact: true }),
    ).toHaveAttribute('aria-pressed', 'true')
    await page.goto(`${root}/catalog/${domain}?view=table`)
    await expect(
      page.getByRole('button', { name: 'Table view', exact: true }),
    ).toHaveAttribute('aria-pressed', 'true')
    await page.goto(`${root}/home/overview`)
    await page.goto(`${root}/catalog/${domain}`)
    await expect(
      page.getByRole('button', { name: 'Table view', exact: true }),
    ).toHaveAttribute('aria-pressed', 'true')
  }
  await page.goto(`${root}/catalog/categories`)
  await page.getByRole('button', { name: 'Spatial view', exact: true }).click()
  await page.goto('/w/studio-berlin/catalog/categories')
  await page.goto(`${root}/catalog/categories`)
  await expect(
    page.getByRole('button', { name: 'Spatial view', exact: true }),
  ).toHaveAttribute('aria-pressed', 'true')
  expect(
    await page.evaluate(() =>
      Object.keys(localStorage).every((key) => key.startsWith('gitstore.')),
    ),
  ).toBe(true)
})
for (const width of [1508, 912, 375])
  test(`filter spacing, chips, sort and view controls agree at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 })
    let size: { width: number; height: number } | undefined
    for (const domain of [
      'products',
      'variants',
      'categories',
      'collections',
    ]) {
      await page.goto(`${root}/catalog/${domain}?status=ready`)
      const button = page.getByRole('button', {
        name: 'Table view',
        exact: true,
      })
      await expect(button).toBeVisible()
      const box = (await button.boundingBox())!
      if (!size) size = box
      else {
        expect(Math.abs(size.width - box.width)).toBeLessThanOrEqual(1)
        expect(Math.abs(size.height - box.height)).toBeLessThanOrEqual(1)
      }
      await expect(
        page.getByRole('button', {
          name: 'Remove status filter: Ready',
          exact: true,
        }),
      ).toBeVisible()
      await page
        .getByRole('button', {
          name: 'Remove status filter: Ready',
          exact: true,
        })
        .click()
      await expect(page).not.toHaveURL(/status=ready/)
      if (width > 760) {
        const fields = page.locator('[data-desktop-filters] > div')
        const boxes = await fields.evaluateAll((items) =>
          items.map((item) => {
            const r = item.getBoundingClientRect()
            return { x: r.x, width: r.width }
          }),
        )
        for (let i = 1; i < boxes.length; i++) {
          expect(
            Math.abs(boxes[0]!.width - boxes[i]!.width),
          ).toBeLessThanOrEqual(1)
          expect(
            boxes[i]!.x - boxes[i - 1]!.x - boxes[i - 1]!.width,
          ).toBeCloseTo(18, 0)
        }
        await expect(
          page.getByRole('button', { name: /Last updated.*Sort/ }),
        ).toBeVisible()
      }
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true)
    }
  })
test('grid cards share Product dimensions while Product rows use separated cards without column headings', async ({
  page,
}) => {
  let card: { width: number; height: number } | undefined,
    mediaHeight = 0
  for (const domain of ['products', 'variants', 'collections']) {
    await page.goto(`${root}/catalog/${domain}?view=grid`)
    const first = page.locator('[data-resource-id]').first()
    await expect(first).toBeVisible()
    const box = (await first.boundingBox())!,
      media = (await first.locator('[data-product-image]').boundingBox())!
    if (!card) {
      card = box
      mediaHeight = media.height
    } else {
      expect(Math.abs(card.width - box.width)).toBeLessThanOrEqual(2)
      expect(Math.abs(mediaHeight - media.height)).toBeLessThanOrEqual(2)
    }
  }
  await page.goto(`${root}/catalog/products?view=table`)
  await expect(page.locator('[class*="tableHeader"]')).toHaveCount(0)
  const a = (await page.locator('[data-resource-id]').nth(0).boundingBox())!,
    b = (await page.locator('[data-resource-id]').nth(1).boundingBox())!
  expect(b.y - a.y - a.height).toBeCloseTo(12, 0)
})
test('search and tag inputs have one keyboard ring and no pointer rings', async ({
  page,
}) => {
  for (const route of [
    'catalog/products',
    'catalog/variants',
    'catalog/categories',
    'catalog/collections',
    'home/recent-activity',
    'catalog/products/new',
  ]) {
    await page.goto(`${root}/${route}`)
    const input = route.endsWith('/new')
      ? page.getByRole('textbox', { name: 'Add tag', exact: true })
      : page.getByRole('searchbox').first()
    await input.click()
    const area = input.locator('xpath=ancestor::*[@data-composite-input][1]')
    await expect(area).toBeVisible()
    expect(
      await input.evaluate((el) => getComputedStyle(el).outlineStyle),
    ).toBe('none')
    expect(await area.evaluate((el) => getComputedStyle(el).outlineStyle)).toBe(
      'none',
    )
    await page.keyboard.press('ArrowLeft')
    expect(
      await input.evaluate((el) => getComputedStyle(el).outlineStyle),
    ).toBe('none')
    expect(await area.evaluate((el) => getComputedStyle(el).outlineWidth)).toBe(
      '2px',
    )
    await input.click()
    expect(await area.evaluate((el) => getComputedStyle(el).outlineStyle)).toBe(
      'none',
    )
  }
})
test('category breadcrumbs start at the root; Product details share the wider canvas and an inner pencil', async ({
  page,
}, testInfo) => {
  await page.goto(`${root}/catalog/categories/tableware`)
  const path = page.getByRole('navigation', { name: 'Category path' })
  await expect(path.getByRole('link')).toHaveText(['Homeware', 'Ceramics'])
  await expect(path.locator('[aria-current="page"]')).toHaveText('Tableware')
  await expect(path.getByRole('link').first()).toHaveAttribute(
    'href',
    `${root}/catalog/categories/homeware`,
  )
  const categoryWidth = (await page.locator('main article').boundingBox())!
    .width
  await page.goto(`${root}/catalog/products/stone-basin`)
  const canvas = page.locator('main article'),
    edit = page.getByRole('link', { name: 'Edit product', exact: true })
  const bounds = (await canvas.boundingBox())!,
    button = (await edit.boundingBox())!
  expect(Math.abs(bounds.width - categoryWidth)).toBeLessThanOrEqual(1)
  expect(button.x).toBeGreaterThan(bounds.x)
  expect(button.x + button.width).toBeLessThan(bounds.x + bounds.width)
  await expect(
    page.getByText('Lifecycle · Active', { exact: true }),
  ).toBeVisible()
  const before = await edit.evaluate(
    (el) => getComputedStyle(el).backgroundColor,
  )
  await edit.hover()
  expect(
    await edit.evaluate((el) => getComputedStyle(el).backgroundColor),
  ).not.toBe(before)
  await page.screenshot({
    path: testInfo.outputPath('product-pencil-hover.png'),
  })
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await edit.click()
  await expect(
    page.getByRole('textbox', { name: 'Title', exact: true }),
  ).toBeVisible()
})
test('category branches unfold, highlight ancestry and Products, and simplify at overview zoom', async ({
  page,
}, testInfo) => {
  await page.goto(`${root}/catalog/categories?view=spatial`)
  const map = page.getByRole('region', { name: 'Category map', exact: true })
  await expect(
    map.getByRole('button', { name: /^Tableware View/ }),
  ).toHaveCount(0)
  await map
    .getByRole('button', { name: 'Unfold Ceramics branch', exact: true })
    .click()
  await expect(
    map.getByRole('button', { name: /^Tableware View/ }),
  ).toBeVisible()
  await map.getByRole('button', { name: /^Homeware View/ }).click()
  await expect(map.locator('[data-path]')).toHaveCount(1)
  await expect(map.getByRole('link')).toHaveCount(4)
  await expect(map.locator('path[data-active]')).toHaveCount(4)
  await page
    .getByRole('button', { name: 'Close inspector', exact: true })
    .click()
  await map.getByRole('button', { name: /^Ceramics View/ }).click()
  await expect(map.locator('[data-path]')).toHaveCount(2)
  await expect(map.locator('path[data-active]')).toHaveCount(1)
  await page.screenshot({
    path: testInfo.outputPath('category-selected-path.png'),
  })
  for (let i = 0; i < 4; i++)
    await map.getByRole('button', { name: 'Zoom out', exact: true }).click()
  await expect(map.locator('[data-detail]')).toHaveAttribute(
    'data-detail',
    'compact',
  )
  expect(
    await map
      .locator('[data-path]')
      .first()
      .evaluate((el) => getComputedStyle(el).animationName),
  ).toBe('none')
})

for (const width of [1508, 892])
  test(`list filters fill the row and clear only active filters at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 })
    for (const path of [
      'catalog/products',
      'catalog/variants',
      'catalog/categories',
      'catalog/collections',
      'home/recent-activity',
    ]) {
      await page.goto(`${root}/${path}`)
      await expect(
        page.getByRole('button', { name: 'Clear filters', exact: true }),
      ).toHaveCount(0)
      const bar = await page.locator('[data-filter-bar]').boundingBox()
      const last = await page
        .locator('[data-desktop-filters] > div')
        .last()
        .boundingBox()
      expect(bar).toBeTruthy()
      expect(last).toBeTruthy()
      if (path === 'home/recent-activity' || width === 892)
        expect(
          Math.abs(last!.x + last!.width - bar!.x - bar!.width),
        ).toBeLessThanOrEqual(1)
      await expect
        .poll(() =>
          page.evaluate(() => {
            const filters = document
              .querySelector('[data-filter-bar]')!
              .getBoundingClientRect()
            const results = document
              .querySelector('[data-filter-results]')!
              .getBoundingClientRect()
            return Math.round(results.top - filters.bottom)
          }),
        )
        .toBe(12)
      await page.getByRole('searchbox').fill('Kanso')
      const clear = page
        .getByRole('button', { name: 'Clear filters', exact: true })
        .first()
      await expect(clear).toBeVisible()
      await expect(clear).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
      await clear.hover()
      await expect(clear).not.toHaveCSS('border-top-color', 'rgba(0, 0, 0, 0)')
      await clear.click()
      await expect(page.getByRole('searchbox')).toHaveValue('')
      await expect(
        page.getByRole('button', { name: 'Clear filters', exact: true }),
      ).toHaveCount(0)
    }
  })
