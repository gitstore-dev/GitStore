import { expect, test } from '@playwright/test'
const root = '/w/atelier-kyoto'
const cases = [
  {
    domain: 'products',
    label: 'Products',
    query: 'q=Kanso&status=blocked&view=grid',
    title: 'Kanso Stone Basin',
  },
  {
    domain: 'variants',
    label: 'Variants',
    query: 'q=Raw&status=failed&view=grid',
    title: 'Raw Silk Tunic — Natural / M',
  },
  {
    domain: 'categories',
    label: 'Categories',
    query: 'q=Tableware&view=tree',
    title: 'Tableware',
  },
  {
    domain: 'collections',
    label: 'Collections',
    query: 'q=Autumn&view=grid&sort=title',
    title: 'Autumn Objects',
  },
]
for (const item of cases)
  test(`${item.label} details expose the same location trail and return to the filtered view`, async ({
    page,
  }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto(`${root}/catalog/${item.domain}?${item.query}`)
    const link = page
      .locator('a[data-product-surface]')
      .filter({ has: page.getByText(item.title, { exact: true }) })
      .first()
    await expect(link).toBeVisible()
    const before = new URL(page.url())
    await link.click()
    const trail = page.getByRole('navigation', {
      name: 'Breadcrumb',
      exact: true,
    })
    await expect(trail.getByRole('link')).toHaveText(['Catalog', item.label])
    await expect(trail.locator('[aria-current="page"]')).toHaveText(item.title)
    await expect(trail.locator('[aria-current="page"]')).toHaveAttribute(
      'title',
      item.title,
    )
    await expect(trail.locator('[aria-current="page"] a')).toHaveCount(0)
    await trail.getByRole('link', { name: item.label, exact: true }).click()
    await expect(page).toHaveURL(new RegExp(`/catalog/${item.domain}\\?`))
    const after = new URL(page.url())
    expect(Object.fromEntries(after.searchParams)).toEqual(
      Object.fromEntries(before.searchParams),
    )
    await expect(page.getByRole('searchbox')).toHaveValue(
      before.searchParams.get('q')!,
    )
  })

test('direct detail links preserve their query, while mobile hides the shell trail', async ({
  page,
}) => {
  await page.goto(`${root}/catalog/categories/tableware?q=Tableware&view=tree`)
  const trail = page.getByRole('navigation', {
    name: 'Breadcrumb',
    exact: true,
  })
  await expect(
    trail.getByRole('link', { name: 'Categories', exact: true }),
  ).toHaveAttribute('href', /q=Tableware/)
  await page.setViewportSize({ width: 375, height: 667 })
  await expect(trail).toBeHidden()
  await expect(
    page.getByRole('button', { name: /Workspace context/ }),
  ).toBeVisible()
  await expect(
    page.locator('main').getByRole('link', { name: 'Categories', exact: true }),
  ).toBeVisible()
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true)
})

test('tight shell trails collapse the middle into a keyboard-accessible menu and restore focus', async ({
  page,
}) => {
  await page.goto(
    `http://localhost:${process.env.STORYBOOK_PORT ?? '6006'}/iframe.html?id=foundation-shell-breadcrumbs--collapsed&viewMode=story`,
  )
  const trail = page.getByRole('navigation', {
    name: 'Breadcrumb',
    exact: true,
  })
  const more = trail.getByRole('button', { name: 'Show navigation ancestors' })
  await expect(more).toBeVisible()
  await expect(trail.getByRole('link')).toHaveText(['Catalog'])
  await more.focus()
  await page.keyboard.press('Enter')
  await expect(
    page.getByRole('menuitem', { name: 'Products', exact: true }),
  ).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(more).toBeFocused()
  await page.keyboard.press('Enter')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('status')).toHaveText('Selected Products')
  await page.locator('[data-breadcrumb-preview]').evaluate((node) => {
    ;(node as HTMLElement).style.width = '700px'
  })
  await expect(more).toHaveCount(0)
  await expect(trail.getByRole('link')).toHaveText(['Catalog', 'Products'])
  await expect(trail.locator('[aria-current="page"]')).toHaveCSS(
    'text-overflow',
    'ellipsis',
  )
})

test('maximum-length titles cannot expand the header or cause horizontal scrolling', async ({
  page,
}) => {
  await page.setViewportSize({ width: 892, height: 763 })
  await page.goto(
    `http://localhost:${process.env.STORYBOOK_PORT ?? '6006'}/iframe.html?id=foundation-shell-breadcrumbs--maximum-title&viewMode=story`,
  )
  const current = page
    .getByRole('navigation', { name: 'Breadcrumb' })
    .locator('[aria-current="page"]')
  await expect(current).toHaveAttribute('title', 'W'.repeat(200))
  await expect(current).toHaveCSS('text-overflow', 'ellipsis')
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true)
})
