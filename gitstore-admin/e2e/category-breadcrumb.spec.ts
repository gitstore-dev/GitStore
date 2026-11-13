import { expect, test } from '@playwright/test'
const storybook = `http://localhost:${process.env.STORYBOOK_PORT ?? '6006'}`

test('category path shares identity styling and keeps full root ancestry', async ({
  page,
}, testInfo) => {
  await page.goto('/w/atelier-kyoto/catalog/categories/tableware')
  const trail = page.getByRole('navigation', {
    name: 'Category path',
    exact: true,
  })
  await expect(trail).toBeVisible()
  await expect(trail.getByRole('link').first()).toHaveText('Homeware')
  await expect(trail.locator('[aria-current="page"]')).toHaveText('Tableware')
  await expect(trail.getByRole('link').first()).toHaveCSS(
    'text-decoration-line',
    'none',
  )
  await expect(trail.getByRole('link').first()).toHaveCSS(
    'text-transform',
    'uppercase',
  )
  await page.screenshot({
    path: testInfo.outputPath('category-path-desktop.png'),
  })
  const back = page
    .getByRole('link', { name: 'Categories', exact: true })
    .last()
  await expect(back).toHaveCSS('text-decoration-line', 'none')
  await trail.getByRole('link', { name: 'Ceramics', exact: true }).click()
  await expect(page).toHaveURL(/categories\/ceramics/)
})

test('deep paths adapt to width and expose omitted ancestors in an ordered keyboard menu', async ({
  page,
}) => {
  await page.goto(
    `${storybook}/iframe.html?id=foundation-category-path--deep&viewMode=story`,
  )
  const trigger = page.getByRole('button', {
    name: 'Show omitted category ancestors',
  })
  await expect(trigger).toBeVisible()
  const frame = page
    .getByRole('navigation', { name: 'Category path', exact: true })
    .locator('..')
  await frame.evaluate((node) => {
    ;(node as HTMLElement).style.width = '1200px'
  })
  await expect(trigger).toHaveCount(0)
  await frame.evaluate((node) => {
    ;(node as HTMLElement).style.width = '600px'
  })
  await expect(trigger).toBeVisible()
  await trigger.focus()
  await page.keyboard.press('Enter')
  const menu = page.getByRole('menu', { name: 'Omitted category ancestors' })
  await expect(menu.getByRole('menuitem').first()).toHaveText('Living')
  await page.keyboard.press('Escape')
  await expect(trigger).toBeFocused()
  await page.keyboard.press('Enter')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('status')).toHaveText('Selected Living')
  await expect(trigger).toBeFocused()
  await expect(page.locator('[aria-current="page"]').first()).toHaveText(
    'Tableware',
  )
})

test('long titles truncate visually and mobile reveals the full ordered path', async ({
  page,
}, testInfo) => {
  await page.goto(
    `${storybook}/iframe.html?id=foundation-category-path--long-title&viewMode=story`,
  )
  const current = page.locator('[aria-current="page"]').first()
  await expect(current).toHaveAttribute('title', /Tableware for thoughtfully/)
  await expect(current).toHaveCSS('text-overflow', 'ellipsis')
  const parent = page
    .getByRole('navigation', { name: 'Category path', exact: true })
    .getByRole('link', { name: 'Ceramics', exact: true })
  expect((await parent.boundingBox())!.width).toBeGreaterThan(35)
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true)
  await page.screenshot({
    path: testInfo.outputPath('category-path-collapsed.png'),
  })
  await page.setViewportSize({ width: 375, height: 667 })
  await expect(
    page.getByRole('navigation', { name: 'Category path', exact: true }),
  ).toBeHidden()
  const disclosure = page
    .locator('details')
    .filter({ has: page.getByText('Category path', { exact: true }) })
  await disclosure.locator('summary').focus()
  await page.keyboard.press('Enter')
  await expect(disclosure).toHaveAttribute('open')
  const fullPath = page.getByRole('navigation', { name: 'Full category path' })
  await expect(fullPath.getByRole('listitem')).toHaveCount(9)
  await expect(fullPath.getByRole('link').first()).toHaveText('Homeware')
  await expect(fullPath.locator('[aria-current]')).toHaveText(
    /Tableware for thoughtfully/,
  )
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true)
  await page.screenshot({
    path: testInfo.outputPath('category-path-mobile.png'),
  })
})
