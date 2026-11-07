import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { storybookURL } from './storybook-url'
const base = '/w/atelier-kyoto/catalog/products'
async function markdown(page: import('@playwright/test').Page) {
  await page.getByText('Markdown', { exact: true }).click()
  return page.getByRole('textbox', {
    name: 'Markdown description',
    exact: true,
  })
}
test('Product create, edit, Markdown source, media order and shared Home catalog work without backend traffic', async ({
  page,
}) => {
  const forbidden: string[] = []
  page.on('request', (r) => {
    if (/graphql|\/api\//i.test(r.url())) forbidden.push(r.url())
  })
  await page.goto(base)
  await expect(page).toHaveTitle('Products · GitStore')
  await page.getByRole('link', { name: 'New product' }).click()
  await page
    .getByRole('textbox', { name: 'Title', exact: true })
    .fill('A handmade demo bowl')
  const source = '# Made with care\n\n*  Natural clay\n*  **Hand finished**\n\n'
  await (await markdown(page)).fill(source)
  await page.getByText('Preview', { exact: true }).click()
  await expect(
    page.getByRole('heading', { name: 'Made with care' }),
  ).toBeVisible()
  await page.getByText('Write', { exact: true }).click()
  await expect(
    page.getByRole('textbox', { name: 'Description', exact: true }),
  ).toBeVisible()
  await page.getByText('Markdown', { exact: true }).click()
  await expect(
    page.getByRole('textbox', { name: 'Markdown description' }),
  ).toHaveValue(source)
  await page.getByRole('button', { name: 'Create demo product' }).click()
  await expect(page).toHaveURL(/products\/a-handmade-demo-bowl/)
  await expect(page).toHaveTitle('A handmade demo bowl · GitStore')
  await expect(page.getByText('Processing', { exact: true })).toBeVisible()
  await page.getByRole('link', { name: 'Edit product', exact: true }).click()
  await page
    .getByRole('textbox', { name: 'Title', exact: true })
    .fill('A revised demo bowl')
  await expect(await markdown(page)).toHaveValue(source)
  await page.getByRole('button', { name: 'Add basin-detail.webp' }).click()
  await page.getByRole('button', { name: 'Add lookbook-hero.webp' }).click()
  await page.getByRole('button', { name: 'Move image 2 earlier' }).click()
  await page.getByRole('button', { name: 'Save demo changes' }).click()
  await expect(page).toHaveURL(/products\/a-handmade-demo-bowl/)
  await expect(page).toHaveTitle('A revised demo bowl · GitStore')
  await expect(page.locator('article img').first()).toHaveAttribute(
    'src',
    '/media/demo/basin-2.jpg',
  )
  // SPA navigation preserves the session; a document reload resets it.
  await page.keyboard.press('Control+k')
  await page
    .getByRole('searchbox', { name: 'Search pages' })
    .fill('home overview')
  await page.getByRole('option', { name: /Overview/ }).click()
  await expect(
    page.getByRole('heading', { name: 'A revised demo bowl', exact: true }),
  ).toBeVisible()
  await page.getByRole('link', { name: /Processing products/ }).click()
  await expect(page).toHaveURL(/status=reconciling/)
  await expect(
    page.getByRole('link', { name: /^View A revised demo bowl(?: |$)/ }),
  ).toBeVisible()
  await page.reload()
  await expect(
    page.getByRole('link', { name: /^View A revised demo bowl(?: |$)/ }),
  ).toHaveCount(0)
  expect(forbidden).toEqual([])
})
test('rich editing supports slash blocks, formatting and undo; keyboard mode switches preserve body', async ({
  page,
}) => {
  await page.goto(`${base}/new`)
  const editor = page.getByRole('textbox', { name: 'Description', exact: true })
  await expect(editor).toBeVisible()
  await expect(page.locator('[data-markdown-ready]')).toHaveAttribute(
    'data-markdown-ready',
    'true',
  )
  await editor.fill('A rich description')
  await editor.press('Meta+a')
  await editor.press('Meta+b')
  await expect(editor.locator('strong')).toHaveText('A rich description')
  await editor.press('Meta+z')
  await expect(editor.locator('strong')).toHaveCount(0)
  await editor.press('Meta+b')
  const source = await markdown(page)
  await expect(source).toHaveValue(/\*\*A rich description\*\*/)
  await page.getByText('Write', { exact: true }).click()
  await expect(editor).toBeVisible()
  await expect(page.locator('[data-markdown-ready]')).toHaveAttribute(
    'data-markdown-ready',
    'true',
  )
  await editor.fill('/')
  await expect(page.getByText('Heading 1', { exact: true })).toBeVisible()
  await editor.press('Escape')
})
test('draft buffers survive navigation and store switches; cancel confirms and restores saved content', async ({
  page,
}) => {
  await page.goto(`${base}/stone-basin/edit`)
  const title = page.getByRole('textbox', { name: 'Title', exact: true })
  await title.fill('Preserved Product edits')
  await page
    .getByRole('link', { name: 'Products', exact: true })
    .first()
    .click()
  await page
    .getByRole('link', { name: /^View Kanso Stone Basin(?: |$)/ })
    .click()
  await page.getByRole('link', { name: 'Edit product', exact: true }).click()
  await expect(title).toHaveValue('Preserved Product edits')
  await page.getByRole('button', { name: 'Cancel', exact: true }).click()
  const dialog = page.getByRole('dialog', {
    name: 'Discard unsaved Product edits?',
  })
  await expect(dialog).toBeVisible()
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(title).toHaveValue('Preserved Product edits')
  await page.getByRole('button', { name: 'Cancel', exact: true }).click()
  await page.getByRole('button', { name: 'Discard edits' }).click()
  await page.getByRole('link', { name: 'Edit product', exact: true }).click()
  await expect(title).toHaveValue('Kanso Stone Basin')
})
test('deletion blocks dependent variants and completes separately with persistent history', async ({
  page,
}) => {
  await page.goto(`${base}/silk-tunic`)
  await expect(
    page.getByRole('button', { name: 'Delete demo product' }),
  ).toBeDisabled()
  await expect(page.getByText(/2 dependent variants/)).toBeVisible()
  await page
    .getByRole('link', { name: 'Products', exact: true })
    .first()
    .click()
  await page
    .getByRole('link', { name: /^View Kanso Stone Basin(?: |$)/ })
    .click()
  await page.getByRole('button', { name: 'Delete demo product' }).click()
  await page.getByRole('button', { name: 'Confirm demo deletion' }).click()
  await expect(
    page.getByRole('heading', { name: 'Deletion in progress' }),
  ).toBeVisible()
  await expect(
    page.getByRole('link', { name: 'Edit product', exact: true }),
  ).toHaveCount(0)
  await page.getByRole('button', { name: 'Complete demo deletion' }).click()
  await page.getByRole('button', { name: 'Confirm completion' }).click()
  await expect(
    page.getByRole('link', { name: /^View Kanso Stone Basin(?: |$)/ }),
  ).toHaveCount(0)
  await expect(
    page.getByRole('status').filter({ hasText: 'Demo product deleted' }),
  ).toBeVisible()
  await page.keyboard.press('Control+k')
  await page
    .getByRole('searchbox', { name: 'Search pages' })
    .fill('recent activity')
  await page.getByRole('option', { name: /Recent activity/ }).click()
  await expect(
    page
      .getByRole('heading', { name: 'Kanso Stone Basin', exact: true })
      .first(),
  ).toBeVisible()
  await page
    .getByRole('button', {
      name: /^View details for Kanso Stone Basin(?: |$)/,
    })
    .first()
    .click()
  await expect(
    page
      .getByRole('complementary', { name: 'Page context' })
      .getByText('Product deleted', { exact: true }),
  ).toBeVisible()
})
test('list filters, direct links, history and URL state survive reload', async ({
  page,
}) => {
  await page.goto(`${base}?view=grid&status=ready&sort=title`)
  await expect(page.getByRole('link', { name: /^View / })).toHaveCount(2)
  await page.getByRole('link', { name: /^View Raw Silk Tunic(?: |$)/ }).click()
  await page.goBack()
  await expect(page).toHaveURL(/status=ready/)
  await page.reload()
  await expect(page.getByRole('link', { name: /^View / })).toHaveCount(2)
  await page
    .getByRole('searchbox', { name: 'Search products' })
    .fill('not a product')
  await expect(
    page.getByRole('heading', { name: 'No matching products' }),
  ).toBeVisible()
})
for (const width of [375, 981, 1440])
  test(`Product confirmations, focus and overflow at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 763 })
    await page.goto(`${base}/stone-basin`)
    await page.getByRole('button', { name: 'Delete demo product' }).click()
    const dialog = page.getByRole('dialog', { name: 'Start demo deletion?' })
    await expect(dialog).toBeInViewport()
    await page.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)
    await expect(
      page.getByRole('button', { name: 'Delete demo product' }),
    ).toBeFocused()
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  })
for (const story of [
  'validation',
  'delete-confirmation',
  'discard-confirmation',
  'unsupported-markdown',
  'stale-editor',
])
  test(`Product Storybook ${story} at narrow canvas`, async ({ page }) => {
    await page.setViewportSize({ width: 981, height: 763 })
    await page.goto(
      `${storybookURL}/?path=/story/pages-catalog-products--${story}`,
    )
    const frame = page.frameLocator('#storybook-preview-iframe')
    if (story === 'unsupported-markdown')
      await expect(
        frame.getByRole('textbox', { name: 'Markdown description' }),
      ).toHaveValue(/<aside>/)
    else if (story === 'stale-editor') {
      await expect(
        frame.getByRole('button', { name: 'Save demo changes' }),
      ).toBeDisabled()
      await expect(frame.getByRole('alert')).toContainText(
        'saved Product has changed',
      )
    } else {
      await page.getByRole('tab', { name: /^Interactions/ }).click()
      await expect(
        page
          .getByRole('toolbar', { name: 'Component test playback controls' })
          .getByText(/^pass$/i),
      ).toBeVisible({ timeout: 20000 })
    }
  })

test('Product editor preserves unsaved text across stores and Focus mode', async ({
  page,
}) => {
  await page.goto(`${base}/stone-basin/edit`)
  const title = page.getByRole('textbox', { name: 'Title', exact: true })
  await title.fill('A preserved store draft')
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await expect(title).toHaveValue('A preserved store draft')
  await page
    .getByRole('button', { name: 'Exit Focus Mode', exact: true })
    .click()
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Studio Berlin/ }).click()
  await expect(
    page.getByRole('heading', {
      name: 'Your catalog starts here',
      exact: true,
    }),
  ).toBeVisible()
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Atelier Kyoto/ }).click()
  await page
    .getByRole('link', { name: /^View Kanso Stone Basin(?: |$)/ })
    .click()
  await page.getByRole('link', { name: 'Edit product', exact: true }).click()
  await expect(title).toHaveValue('A preserved store draft')
  await page.reload()
  await expect(title).toHaveValue('Kanso Stone Basin')
  await expect(
    page.getByRole('button', { name: 'Save demo changes' }),
  ).toBeDisabled()
})
test('Product pagination and confirmed store reset use the shared catalog', async ({
  page,
}) => {
  await page.goto(base)
  for (let index = 1; index <= 5; index++) {
    await page.getByRole('link', { name: 'New product', exact: true }).click()
    await page
      .getByRole('textbox', { name: 'Title', exact: true })
      .fill(`Pagination example ${index}`)
    await page.getByRole('button', { name: 'Create demo product' }).click()
    await page
      .getByRole('link', { name: 'Products', exact: true })
      .first()
      .click()
  }
  await expect(page.getByRole('link', { name: /^View / })).toHaveCount(8)
  await page
    .getByRole('button', { name: 'Load more products', exact: true })
    .click()
  await expect(page.getByRole('link', { name: /^View / })).toHaveCount(9)
  await expect(page.getByRole('link', { name: /^View / }).nth(8)).toBeFocused()
  await expect(
    page.getByRole('button', { name: 'Load more products', exact: true }),
  ).toHaveCount(0)
  await page
    .getByRole('searchbox', { name: 'Search products' })
    .fill('Pagination example')
  await expect(page.getByRole('link', { name: /^View / })).toHaveCount(5)
  await page.getByRole('searchbox', { name: 'Search products' }).fill('')
  await expect(page.getByRole('link', { name: /^View / })).toHaveCount(8)
  await page.getByRole('button', { name: 'Demo data', exact: true }).click()
  await page.getByRole('button', { name: 'Reset demo', exact: true }).click()
  await page
    .getByRole('button', { name: 'Confirm demo reset', exact: true })
    .click()
  await expect(page.getByRole('link', { name: /^View / })).toHaveCount(4)
})
test('retirement is a saved lifecycle change, and variants remain read-only', async ({
  page,
}) => {
  await page.goto(`${base}/silk-tunic/edit`)
  await page.getByRole('button', { name: /Lifecycle$/ }).click()
  await page.getByRole('option', { name: 'Retired', exact: true }).click()
  await page.getByRole('button', { name: 'Save demo changes' }).click()
  await expect(page.getByText('Retired', { exact: true })).toBeVisible()
  await expect(page.getByText('Processing', { exact: true })).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Delete demo product' }),
  ).toBeDisabled()
  await expect(
    page.getByText('2 linked · Read only', { exact: true }),
  ).toBeVisible()
})
test('stale Product editor confirms reload without changing personal work', async ({
  page,
}) => {
  await page.goto(
    `${storybookURL}/iframe.html?id=pages-catalog-products--stale-editor&viewMode=story`,
  )
  await expect(
    page.getByRole('button', { name: 'Save demo changes' }),
  ).toBeDisabled()
  await page.getByRole('button', { name: 'Reload saved product' }).click()
  await page
    .getByRole('dialog', { name: 'Reload saved Product?' })
    .getByRole('button', { name: 'Cancel', exact: true })
    .click()
  await expect(await markdown(page)).toHaveValue('Preserved unsaved edits.')
  await page.getByRole('button', { name: 'Reload saved product' }).click()
  await page.getByRole('button', { name: 'Confirm reload' }).click()
  await expect(await markdown(page)).toHaveValue('A recovered saved version.')
})
