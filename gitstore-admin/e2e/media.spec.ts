import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { storybookURL } from './storybook-url'
const root = '/w/atelier-kyoto/catalog/media'
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
})
test('Media cards, details, breadcrumbs, keyboard return, reload and unavailable contexts', async ({
  page,
}) => {
  const backend: string[] = []
  page.on('request', (r) => {
    if (/graphql|\/api\/|^demo:/.test(r.url())) backend.push(r.url())
  })
  await page.goto(root)
  await expect(page).toHaveTitle('Media · GitStore')
  await expect(
    page.getByRole('button', { name: 'Cards view' }),
  ).toHaveAttribute('aria-pressed', 'true')
  await expect(page.locator('[data-resource-id]')).toHaveCount(2)
  const card = page.locator('[data-resource-id="basin-image"]')
  await card.focus()
  await card.press('Enter')
  await expect(page).toHaveURL(/media\/basin-image/)
  await expect(page.locator('main h1')).toBeFocused()
  await expect(page).toHaveTitle('basin-detail.webp · GitStore')
  const trail = page.getByRole('navigation', {
    name: 'Breadcrumb',
    exact: true,
  })
  await expect(trail.getByRole('link')).toHaveText(['Catalog', 'Media'])
  await expect(page.locator('main [data-page-context]')).toContainText(
    'Atelier Kyoto',
  )
  await expect(
    page.getByRole('heading', { name: 'Used by', exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('link', { name: 'Kanso Stone Basin', exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('link', { name: 'Ceramics', exact: true }),
  ).toBeVisible()
  await page.getByText('Technical details', { exact: true }).click()
  await expect(
    page.getByText('media/basin-detail.md', { exact: true }),
  ).toBeVisible()
  await expect(page.locator('main')).toContainText(
    'demo://studio/basin-detail.webp',
  )
  await expect(
    page.getByRole('button', { name: /^(Upload|Edit|Delete|Regenerate)/ }),
  ).toHaveCount(0)
  await page.reload()
  await expect(page.locator('main h1')).toHaveText('basin-detail.webp')
  await page.goBack()
  await expect(card).toBeFocused()
  await expect(page.locator('[data-resource-id]')).toHaveCount(2)
  await page.goForward()
  await expect(page.locator('main h1')).toHaveText('basin-detail.webp')
  await page.goto(root + '/missing')
  await expect(
    page.getByRole('heading', { name: 'Media unavailable' }),
  ).toBeVisible()
  await page.goto('/w/studio-berlin/catalog/media')
  await expect(
    page.getByRole('heading', { name: 'Your media library starts here' }),
  ).toBeVisible()
  expect(backend).toEqual([])
})
test('filters, URL search, independent empty results, display preference and mobile filter staging', async ({
  page,
}) => {
  await page.goto(root)
  await page.getByRole('button', { name: 'Rows view' }).click()
  await page.getByRole('searchbox', { name: 'Search media' }).fill('basin')
  await expect(page.locator('[data-resource-id]')).toHaveCount(1)
  await page.reload()
  await expect(page.getByRole('searchbox')).toHaveValue('basin')
  await page.getByRole('searchbox').fill('missing')
  await expect(
    page.getByRole('heading', { name: 'No matching media' }),
  ).toBeVisible()
  await page
    .locator('[data-filter-results]')
    .getByRole('button', { name: 'Clear filters', exact: true })
    .click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(2)
  await page.goto('/w/atelier-kyoto/home/overview')
  await page.goto(root)
  await expect(page.getByRole('button', { name: 'Rows view' })).toHaveAttribute(
    'aria-pressed',
    'true',
  )
  await page.setViewportSize({ width: 375, height: 667 })
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Filter media' })
  await sheet.getByRole('button', { name: /Status$/ }).click()
  await page.getByRole('option', { name: 'Ready', exact: true }).click()
  await sheet.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(2)
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  await sheet.getByRole('button', { name: /Status$/ }).click()
  await page.getByRole('option', { name: 'Ready', exact: true }).click()
  await sheet
    .getByRole('button', { name: 'Apply filters', exact: true })
    .click()
  await expect(page).toHaveURL(/status=ready/)
  await expect(
    page.getByRole('heading', { name: 'No matching media' }),
  ).toBeVisible()
  await page
    .locator('[data-filter-results]')
    .getByRole('button', { name: 'Clear filters' })
    .click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(2)
})
for (const width of [1440, 900, 375])
  test(`enlarged preview stays above responsive inspector and restores focus at ${width}px`, async ({
    page,
  }, info) => {
    await page.setViewportSize({ width, height: 900 })
    await page.goto(root + '/lookbook')
    await page.getByRole('button', { name: 'Inspector', exact: true }).click()
    const trigger = page.getByRole('button', { name: 'Enlarge preview' })
    if (width <= 1100) {
      await page
        .getByRole('button', { name: 'Close inspector', exact: true })
        .click()
    }
    await trigger.focus()
    await trigger.press('Enter')
    const dialog = page.getByRole('dialog', {
      name: 'Preview lookbook-hero.webp',
    })
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole('img')).toBeVisible()
    await dialog.getByRole('button', { name: '100%', exact: true }).click()
    await expect(dialog.getByRole('button', { name: '100%' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    await dialog.getByRole('button', { name: 'Fit', exact: true }).click()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.screenshot({
      path: info.outputPath(`media-preview-${width}.png`),
    })
    await page.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)
    await expect(trigger).toBeFocused()
    await expect(
      page.getByRole('heading', { name: 'Requested sizes' }),
    ).toBeVisible()
    await expect(
      page.getByRole('heading', { name: 'Available demo outputs' }),
    ).toBeVisible()
  })
test('media references link from Products, Categories, Overview and history without losing shared state', async ({
  page,
}) => {
  await page.goto('/w/atelier-kyoto/catalog/products/stone-basin')
  await page
    .getByRole('link', { name: 'Open media basin-detail.webp', exact: true })
    .click()
  await expect(page).toHaveURL(/media\/basin-image/)
  await page.getByRole('link', { name: 'Ceramics', exact: true }).click()
  await page.getByRole('link', { name: 'Open category media file' }).click()
  await expect(page).toHaveURL(/media\/basin-image/)
  await page.goto('/w/atelier-kyoto/home/overview')
  await page
    .getByRole('button', { name: 'View details for basin-detail.webp' })
    .click()
  await page.getByRole('link', { name: 'Open media', exact: true }).click()
  await expect(page).toHaveURL(/media\/basin-image/)
  await page.goto(root)
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Studio Berlin/ }).click()
  await expect(
    page.getByRole('heading', { name: 'Your media library starts here' }),
  ).toBeVisible()
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Atelier Kyoto/ }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(2)
})
test('bounded Load more and card return preserve loaded media, focus and filters', async ({
  page,
}) => {
  await page.goto(
    `${storybookURL}/iframe.html?id=pages-catalog-media--more-results&viewMode=story`,
  )
  await expect(page.locator('[data-resource-id]')).toHaveCount(6)
  await page.getByRole('button', { name: 'Load more media' }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(12)
  await expect(
    page.getByRole('status').filter({ hasText: 'More media files loaded.' }),
  ).toBeVisible()
  const card = page.locator('[data-resource-id]').nth(11)
  const selectedName = await card.locator('[data-product-title]').innerText()
  await card.focus()
  await card.press('Enter')
  await expect(page.locator('main h1')).toHaveText(selectedName)
  await page.getByRole('link', { name: 'Media', exact: true }).last().click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(12)
  await expect(card).toBeFocused()
  await page.getByRole('button', { name: 'Load more media' }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(15)
  await expect(
    page.getByRole('button', { name: 'Load more media' }),
  ).toHaveCount(0)
  await page.getByRole('searchbox').fill('example 13')
  await expect(page.locator('[data-resource-id]')).toHaveCount(1)
})
for (const [story, expected] of [
  ['missing-preview', 'No media preview'],
  ['failed-preview', 'Preview unavailable'],
  ['non-image-file', 'No media preview'],
  ['unused', 'Not used by any items in this demo.'],
  ['loading', 'Loading media'],
  ['unavailable', 'Media unavailable'],
] as const)
  test(`Media ${story} presentation is honest and keeps inspection available`, async ({
    page,
  }) => {
    await page.goto(
      `${storybookURL}/iframe.html?id=pages-catalog-media--${story}&viewMode=story`,
    )
    await expect(page.locator('main')).toContainText(expected)
    await expect(
      page.getByRole('button', { name: /^(Upload|Edit|Delete|Regenerate)/ }),
    ).toHaveCount(0)
    if (story.includes('preview') || story === 'non-image-file') {
      await expect(
        page.getByRole('button', { name: 'Enlarge preview' }),
      ).toHaveCount(0)
      await expect(
        page.getByText('Technical details', { exact: true }),
      ).toBeVisible()
    }
  })
for (const width of [1440, 892, 375])
  for (const theme of ['light', 'dark'] as const)
    test(`Media list and detail fit ${width}px in ${theme}`, async ({
      page,
    }, info) => {
      await page.setViewportSize({ width, height: 900 })
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      for (const view of ['grid', 'table']) {
        await page.goto(`${root}?view=${view}`)
        await expect(page.locator('[data-media-list]')).toBeVisible()
        if (width === 375)
          expect(
            (await page.getByRole('searchbox').boundingBox())!.width,
          ).toBeGreaterThan(280)
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        ).toBe(true)
        expect((await new AxeBuilder({ page }).analyze()).violations).toEqual(
          [],
        )
        await page.screenshot({
          path: info.outputPath(`media-${view}-${width}-${theme}.png`),
          fullPage: true,
        })
      }
      await page.locator('[data-resource-id="lookbook"]').click()
      await expect(page.locator('main h1')).toBeVisible()
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true)
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
      await page.screenshot({
        path: info.outputPath(`media-detail-${width}-${theme}.png`),
        fullPage: true,
      })
      await page
        .getByRole('button', { name: 'Focus mode', exact: true })
        .click()
      await expect(page.locator('main h1')).toHaveText('lookbook-hero.webp')
      await page
        .getByRole('button', { name: 'Exit Focus Mode', exact: true })
        .click()
      await expect(page.locator('main h1')).toHaveText('lookbook-hero.webp')
    })
for (const story of ['preview-dialog', 'preview-keyboard'])
  test(`Media ${story} Storybook interaction completes`, async ({ page }) => {
    await page.goto(
      `${storybookURL}/?path=/story/pages-catalog-media--${story}`,
    )
    await page.getByRole('tab', { name: /^Interactions/ }).click()
    await expect(
      page
        .getByRole('toolbar', { name: 'Component test playback controls' })
        .getByText(/^pass$/i),
    ).toBeVisible({ timeout: 20000 })
  })

test('Overview File changes, Media reset, history links and reload share the demo session', async ({
  page,
}) => {
  await page.goto('/w/atelier-kyoto/home/overview')
  await page
    .getByRole('button', {
      name: 'View details for basin-detail.webp',
      exact: true,
    })
    .click()
  await page
    .getByRole('button', { name: 'Apply demo fix', exact: true })
    .click()
  await page
    .getByRole('dialog', { name: 'Apply demo fix?' })
    .getByRole('button', { name: 'Confirm demo change' })
    .click()
  await page.getByRole('link', { name: 'Open media', exact: true }).click()
  await expect(page.locator('main h1')).toHaveText('basin-detail.webp')
  await expect(
    page.locator('main').getByText('Ready', { exact: true }),
  ).toBeVisible()
  await page.getByRole('link', { name: 'Media', exact: true }).last().click()
  await page.getByRole('button', { name: /Status$/ }).click()
  await page.getByRole('option', { name: 'Ready', exact: true }).click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(1)
  await page.getByRole('button', { name: 'Demo data', exact: true }).click()
  await page.getByRole('button', { name: 'Reset demo', exact: true }).click()
  await page
    .getByRole('dialog', { name: /Reset catalog and Home demo/ })
    .getByRole('button', { name: 'Cancel', exact: true })
    .click()
  await expect(page.locator('[data-resource-id]')).toHaveCount(1)
  await page.getByRole('button', { name: 'Demo data', exact: true }).click()
  await page.getByRole('button', { name: 'Reset demo', exact: true }).click()
  await page
    .getByRole('button', { name: 'Confirm demo reset', exact: true })
    .click()
  await expect(
    page.getByRole('heading', { name: 'No matching media' }),
  ).toBeVisible()
  await page.reload()
  await expect(
    page.getByRole('heading', { name: 'No matching media' }),
  ).toBeVisible()
  await page.goto('/w/atelier-kyoto/home/recent-activity')
  await page
    .getByRole('searchbox', { name: 'Search activity' })
    .fill('lookbook')
  await page
    .getByRole('button', {
      name: 'View details for lookbook-hero.webp',
      exact: true,
    })
    .first()
    .click()
  await page.getByRole('link', { name: 'Open current media file' }).click()
  await expect(page).toHaveURL(/media\/lookbook/)
  await expect(page.locator('main h1')).toHaveText('lookbook-hero.webp')
})
