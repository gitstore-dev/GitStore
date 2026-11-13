import { expect, test } from '@playwright/test'
for (const theme of ['light', 'dark'] as const)
  test(`Product, Variant and Collection media fill the hovered card in ${theme}`, async ({
    page,
  }, testInfo) => {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    for (const domain of ['products', 'variants', 'collections']) {
      await page.goto(`/w/atelier-kyoto/catalog/${domain}?view=grid`)
      const cards = page.locator('[data-catalog-card]')
      await expect(cards.first()).toBeVisible()
      const count = await cards.count()
      expect(count).toBeGreaterThan(0)
      // Exercise real images and placeholders, without changing the fixtures.
      await page.evaluate(() => document.fonts.ready)
      for (const card of await cards.all()) {
        await card.scrollIntoViewIfNeeded()
        await page.mouse.move(0, 0)
        const backdrop = card.locator('[data-card-backdrop]')
        await expect(backdrop).toHaveCSS('opacity', '0')
        const before = (await card.boundingBox())!
        await card.hover()
        await expect(backdrop).toHaveCSS('opacity', '1')
        await expect(card.locator('[data-product-image]')).toHaveCSS(
          'opacity',
          '0',
        )
        const full = (await backdrop.boundingBox())!
        const interior = await card.evaluate((node) => ({
          width: node.clientWidth,
          height: node.clientHeight,
        }))
        expect(Math.abs(full.width - interior.width)).toBeLessThanOrEqual(1)
        expect(Math.abs(full.height - interior.height)).toBeLessThanOrEqual(1)
        const media = (await backdrop.locator(':scope > div').boundingBox())!
        expect(Math.abs(media.height - full.height)).toBeLessThanOrEqual(1)
        expect(await card.boundingBox()).toEqual(before)
        await expect(card.locator('[data-product-title]')).toBeVisible()
        if (await backdrop.locator('img').count())
          await page.screenshot({
            path: testInfo.outputPath(`${domain}-${theme}-image-hover.png`),
          })
      }
      await page.screenshot({
        path: testInfo.outputPath(`${domain}-${theme}-card-hover.png`),
      })
      await page.mouse.move(0, 0)
      const card = cards.first()
      await card.focus()
      await expect(card.locator('[data-card-backdrop]')).toHaveCSS(
        'opacity',
        '1',
      )
      await expect(card.locator('[data-card-backdrop]')).toHaveCSS(
        'transform',
        'none',
      )
      expect(
        await card
          .locator('[data-card-backdrop]')
          .evaluate((node) =>
            parseFloat(getComputedStyle(node).transitionDuration),
          ),
      ).toBeLessThanOrEqual(0.001)
    }
  })

test('card-media Storybook hover demonstrates the shared full-card image', async ({
  page,
}) => {
  await page.goto(
    `http://localhost:${process.env.STORYBOOK_PORT ?? '6006'}/iframe.html?id=foundation-catalog-card-media--image&viewMode=story`,
  )
  await page.getByRole('link').hover()
  await expect(page.locator('[data-card-backdrop]')).toHaveCSS('opacity', '1')
})

for (const width of [1508, 892, 375])
  test(`catalog row thumbnails share Product sizing at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ reducedMotion: 'reduce' })
    for (const domain of ['products', 'variants', 'collections']) {
      await page.goto(`/w/atelier-kyoto/catalog/${domain}?view=table`)
      const rows = page.locator('a[data-product-surface]')
      await expect(rows.first()).toBeVisible()
      for (const row of await rows.all()) {
        const thumbnail = row.locator(
          '[data-product-image] > div[data-compact]',
        )
        await expect(thumbnail).toBeVisible()
        const box = (await thumbnail.boundingBox())!
        expect(box.width).toBe(56)
        expect(box.height).toBe(56)
        await expect(row.locator('[data-product-title]')).toBeVisible()
        await expect(row.locator('[data-card-backdrop]')).toHaveCount(0)
      }
      if (domain === 'products')
        await expect(
          page.locator(
            'a[data-product-surface="stone-basin"] [data-product-image] img',
          ),
        ).toBeVisible()
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true)
    }
    await page.goto('/w/atelier-kyoto/catalog/categories?view=table')
    await expect(page.locator('a[data-product-surface]').first()).toBeVisible()
    await expect(
      page.locator('a[data-product-surface] [data-product-image]'),
    ).toHaveCount(0)
  })
