import { test, expect } from '@playwright/test'
import type { Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { storybookURL } from './storybook-url'
import { openDemoReset } from './home-interactions'
const root = '/w/atelier-kyoto/catalog'
async function choose(page: Page, label: string, value: string) {
  await page.getByRole('button', { name: new RegExp(`${label}$`) }).click()
  await page.getByRole('option', { name: value, exact: true }).click()
}
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
})
for (const domain of ['variants', 'categories', 'collections'] as const) {
  const title = domain[0]!.toUpperCase() + domain.slice(1)
  const item =
    domain === 'variants'
      ? 'tunic-natural-m'
      : domain === 'categories'
        ? 'homeware'
        : 'autumn-objects'
  test(`${domain} lists, details, direct links, reload, history and read-only controls`, async ({
    page,
  }) => {
    const backend: string[] = []
    page.on('request', (r) => {
      if (/graphql|\/api\//.test(r.url())) backend.push(r.url())
    })
    await page.goto(`${root}/${domain}?view=table`)
    await expect(page).toHaveTitle(`${title} · GitStore`)
    const rows = page.locator('[data-resource-id]')
    await expect(rows).toHaveCount(domain === 'categories' ? 4 : 3)
    const link = page.locator(`[data-resource-id="${item}"]`)
    await link.click()
    await expect(page).toHaveURL(new RegExp(`/${domain}/${item}`))
    await expect(page.locator('main h1')).toBeFocused()
    const heading = await page.locator('main h1').textContent()
    await expect(page).toHaveTitle(`${heading} · GitStore`)
    await expect(
      page.getByRole('button', { name: /^(Create|Save|Delete|Publish)/ }),
    ).toHaveCount(0)
    await page.reload()
    await expect(page.locator('main h1')).toContainText(heading!)
    await page.goBack()
    await expect(rows).toHaveCount(domain === 'categories' ? 4 : 3)
    await page.goForward()
    await expect(page.locator('main h1')).toContainText(heading!)
    await page.goto(`/w/studio-berlin/catalog/${domain}`)
    await expect(
      page.getByRole('heading', { name: `No ${domain} yet` }),
    ).toBeVisible()
    await page.goto(`${root}/${domain}/missing-item`)
    await expect(
      page.getByRole('heading', { name: /unavailable/ }),
    ).toBeVisible()
    expect(backend).toEqual([])
  })
  test(`${domain} search, URL filters, mobile sheet and independent empty results`, async ({
    page,
  }) => {
    await page.goto(`${root}/${domain}?view=table`)
    await page
      .getByRole('searchbox', { name: `Search ${domain}` })
      .fill('unmatched')
    await expect(
      page.getByRole('heading', { name: `No matching ${domain}` }),
    ).toBeVisible()
    await page
      .getByRole('button', { name: 'Clear filters', exact: true })
      .click()
    if (domain === 'variants') {
      await page
        .getByRole('searchbox', { name: 'Search variants' })
        .fill('TUNIC-BLK')
      await expect(page.locator('[data-resource-id]')).toHaveCount(1)
      await page.getByRole('searchbox', { name: 'Search variants' }).fill('')
    }
    await choose(page, 'Status', 'Ready')
    await expect(page).toHaveURL(/status=ready/)
    await page
      .getByRole('button', { name: 'Clear filters', exact: true })
      .click()
    await page.setViewportSize({ width: 375, height: 667 })
    await page.getByRole('button', { name: 'Filters', exact: true }).click()
    const sheet = page.getByRole('dialog', { name: `Filter ${domain}` })
    await sheet.getByRole('button', { name: /Status$/ }).click()
    await page.getByRole('option', { name: 'Ready', exact: true }).click()
    await sheet.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(page).not.toHaveURL(/status=ready/)
    await page.getByRole('button', { name: 'Filters', exact: true }).click()
    await sheet.getByRole('button', { name: /Status$/ }).click()
    await page.getByRole('option', { name: 'Ready', exact: true }).click()
    await sheet
      .getByRole('button', { name: 'Apply filters', exact: true })
      .click()
    await expect(page).toHaveURL(/status=ready/)
    await page.reload()
    await expect(page).toHaveURL(/status=ready/)
  })
  test(`${domain} bounded Load more and list-return state work in Storybook`, async ({
    page,
  }) => {
    await page.goto(
      `${storybookURL}/iframe.html?id=pages-catalog-${domain}--more-results&viewMode=story`,
    )
    await expect(page.locator('[data-resource-id]')).toHaveCount(6)
    await page.getByRole('button', { name: 'Load more', exact: true }).click()
    await expect(page.locator('[data-resource-id]')).toHaveCount(12)
    await page.locator('[data-resource-id]').last().click()
    await expect(page.locator('main h1')).toBeVisible()
    await page.getByRole('link', { name: title, exact: true }).last().click()
    await expect(page.locator('[data-resource-id]')).toHaveCount(12)
  })
}
test('category tree, spatial selection, keyboard pan/zoom, Focus and inspector dismissal preserve state', async ({
  page,
}) => {
  await page.goto(`${root}/categories`)
  await page
    .getByRole('button', { name: 'Collapse Homeware', exact: true })
    .click()
  await expect(page.locator('[data-resource-id="ceramics"]')).toHaveCount(0)
  await page
    .getByRole('button', { name: 'Expand Homeware', exact: true })
    .click()
  await expect(page.locator('[data-resource-id="tableware"]')).toBeVisible()
  await page.getByRole('button', { name: 'Spatial view', exact: true }).click()
  const map = page.getByRole('region', { name: 'Pan and zoom categories' })
  await map.focus()
  await page.keyboard.press('+')
  await expect(page.getByText('115%', { exact: true })).toBeVisible()
  await page.keyboard.press('ArrowRight')
  await page.keyboard.press('0')
  await page
    .getByRole('button', { name: /^Ceramics View category guidance/ })
    .click()
  await expect(page).toHaveURL(/selected=ceramics/)
  await expect(page.locator('#category-map-heading')).toBeFocused()
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await expect(map).toBeVisible()
  await page
    .getByRole('button', { name: 'Exit Focus Mode', exact: true })
    .click()
  await expect(page).toHaveURL(/selected=ceramics/)
  await page.getByRole('button', { name: 'Inspector', exact: true }).click()
  await expect(page.locator('#category-map-heading')).toBeVisible()
  await page
    .getByRole('button', { name: 'Close inspector', exact: true })
    .click()
  await expect(page).not.toHaveURL(/selected=/)
  await page.goBack()
  await expect(page).toHaveURL(/view=/)
})
test('collection rules follow shared saved Products while recorded membership remains separate', async ({
  page,
}) => {
  await page.goto(`${root}/collections/autumn-objects`)
  await expect(
    page.getByRole('heading', { name: 'Product rules', exact: true }),
  ).toBeVisible()
  await expect(page.getByText('assortment', { exact: true })).toBeVisible()
  await page
    .getByRole('link', { name: 'Kanso Stone Basin', exact: false })
    .first()
    .click()
  await page.getByRole('link', { name: 'Edit product', exact: true }).click()
  await page
    .getByRole('textbox', { name: 'Title', exact: true })
    .fill('Basin revised')
  await page
    .getByRole('button', { name: 'Save demo changes', exact: true })
    .click()
  await page.keyboard.press('ControlOrMeta+k')
  await page
    .getByRole('searchbox', { name: 'Search pages' })
    .fill('Collections')
  await page
    .getByRole('option', { name: 'Collections Catalog', exact: true })
    .click()
  await page.locator('[data-resource-id="autumn-objects"]').click()
  await expect(
    page.getByRole('link', { name: 'Basin revised', exact: false }).first(),
  ).toBeVisible()
  await expect(page.getByText('assortment', { exact: true })).toBeVisible()
  await openDemoReset(page)
  await page
    .getByRole('dialog')
    .getByRole('button', { name: 'Confirm demo reset', exact: true })
    .click()
  await expect(
    page.getByRole('link', { name: 'Kanso Stone Basin', exact: false }).first(),
  ).toBeVisible()
})
for (const width of [1440, 900, 375])
  for (const theme of ['light', 'dark'] as const) {
    test(`catalog browsers fit and remain accessible at ${width}px in ${theme}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 900 })
      await page.goto(`${root}/variants`)
      if (theme === 'dark') {
        await page.evaluate(() =>
          localStorage.setItem('gitstore.theme', 'dark'),
        )
        await page.reload()
      }
      // Use the existing preference control so this also covers the real theme path.
      if (width > 1100)
        await page
          .getByText(theme === 'dark' ? 'Dark' : 'Light', { exact: true })
          .click()
      for (const domain of ['variants', 'categories', 'collections']) {
        await page.goto(`${root}/${domain}`)
        await expect(page.locator('main h1')).toBeVisible()
        expect((await new AxeBuilder({ page }).analyze()).violations).toEqual(
          [],
        )
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        ).toBe(true)
        await page.screenshot({
          path: testInfo.outputPath(`${domain}-${width}-${theme}.png`),
          fullPage: true,
        })
      }
      await page.goto(`${root}/categories?view=spatial`)
      await expect(
        page.getByRole('region', { name: 'Category map', exact: true }),
      ).toBeVisible()
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
      await page.screenshot({
        path: testInfo.outputPath(`category-map-${width}-${theme}.png`),
      })
      for (const [domain, id] of [
        ['variants', 'tunic-natural-m'],
        ['categories', 'homeware'],
        ['collections', 'autumn-objects'],
      ]) {
        await page.goto(`${root}/${domain}/${id}`)
        await expect(page.locator('main h1')).toBeVisible()
        expect((await new AxeBuilder({ page }).analyze()).violations).toEqual(
          [],
        )
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        ).toBe(true)
        await page.screenshot({
          path: testInfo.outputPath(`${domain}-detail-${width}-${theme}.png`),
          fullPage: true,
        })
      }
    })
  }
test('missing relationships, cycles, empty selectors and unavailable presentation specimens are honest', async ({
  page,
}) => {
  for (const [story, text] of [
    ['categories--cycle', 'Category hierarchy contains a cycle'],
    ['categories--missing-parent', 'Parent category unavailable'],
    [
      'collections--no-rules',
      'No product rules. An empty selector matches no products.',
    ],
    ['collections--missing-member', 'Product unavailable'],
    ['variants--loading', 'Loading variants'],
    ['collections--unavailable', 'Collections unavailable'],
  ]) {
    await page.goto(
      `${storybookURL}/iframe.html?id=pages-catalog-${story}&viewMode=story`,
    )
    await expect(page.getByText(text!, { exact: false }).first()).toBeVisible()
  }
})
test('Overview health columns link to the new filtered indexes and Setup guide opens Categories', async ({
  page,
}) => {
  await page.goto('/w/atelier-kyoto/home/overview')
  await page
    .getByRole('link', { name: '4 Ready categories', exact: true })
    .click()
  await expect(page).toHaveURL(/categories.*status=ready/)
  await page.goto('/w/atelier-kyoto/home/setup-guide?step=categories')
  await page.getByRole('link', { name: 'Open categories', exact: true }).click()
  await expect(page.locator('main h1')).toHaveText('Categories.')
})

for (const [domain, id] of [
  ['variants', 'tunic-natural-m'],
  ['categories', 'homeware'],
  ['collections', 'autumn-objects'],
]) {
  test(`${domain} reuses native list/detail snapshots and restores focus on Back`, async ({
    page,
  }) => {
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await page.goto(`${root}/${domain}?view=table`)
    await page.evaluate(() => {
      const doc = document as Document & { catalogSnapshots?: number }
      doc.catalogSnapshots = 0
      const native = doc.startViewTransition.bind(doc)
      doc.startViewTransition = (
        ...args: Parameters<Document['startViewTransition']>
      ) => {
        if (document.documentElement.dataset.productTransition)
          doc.catalogSnapshots!++
        return native(...args)
      }
    })
    const row = page.locator(`[data-product-surface="${id}"]`)
    await row.focus()
    await row.press('Enter')
    await expect(page.locator('main h1')).toBeFocused()
    await expect(page.locator('html')).not.toHaveAttribute(
      'data-product-transition',
    )
    await expect
      .poll(() =>
        page.evaluate(
          () =>
            (document as Document & { catalogSnapshots?: number })
              .catalogSnapshots,
        ),
      )
      .toBe(1)
    await page.goBack()
    await expect(row).toBeFocused()
    await expect(page.locator('html')).not.toHaveAttribute(
      'data-product-transition',
    )
    await expect
      .poll(() =>
        page.evaluate(
          () =>
            (document as Document & { catalogSnapshots?: number })
              .catalogSnapshots,
        ),
      )
      .toBe(2)
  })
}
for (const width of [1440, 981])
  test(`spatial keyboard Storybook interaction completes at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.goto(
      `${storybookURL}/?path=/story/pages-catalog-categories--spatial-keyboard`,
    )
    await page.getByRole('tab', { name: /^Interactions/ }).click()
    await expect(
      page
        .getByRole('toolbar', { name: 'Component test playback controls' })
        .getByText(/^pass$/i),
    ).toBeVisible({ timeout: 20000 })
  })

for (const width of [900, 375])
  test(`map inspector opens full details and restores focus on dismissal at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.goto(`${root}/categories?view=spatial`)
    const node = page.getByRole('button', {
      name: /^Homeware View category guidance/,
    })
    await node.click()
    await page
      .getByRole('button', { name: 'Inspect selected category', exact: true })
      .click()
    const inspector = page.getByRole('dialog', { name: 'Page context' })
    await expect(
      inspector.getByRole('heading', { name: 'Homeware', exact: true }),
    ).toBeFocused()
    await inspector
      .getByRole('button', { name: 'Close inspector', exact: true })
      .click()
    await expect(node).toBeFocused()
    await expect(page).not.toHaveURL(/selected=/)
    await node.click()
    await page
      .getByRole('button', { name: 'Inspect selected category', exact: true })
      .click()
    await inspector
      .getByRole('link', { name: 'Open category', exact: false })
      .click()
    await expect(page.locator('main h1')).toHaveText('Homeware')
    await expect(inspector).toHaveCount(0)
  })
