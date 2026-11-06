import { expect, test } from '@playwright/test'
import type { Locator } from '@playwright/test'
const pages = [
  {
    id: 'overview',
    title: 'Overview',
    action: 'Apply demo fix',
    dialog: 'Apply demo fix?',
    confirm: 'Confirm demo change',
  },
  {
    id: 'my-work',
    title: 'My work',
    action: 'Submit demo draft',
    dialog: 'Submit demo draft?',
    confirm: 'Confirm demo action',
  },
] as const
async function receivesPointer(locator: Locator) {
  await expect
    .poll(() =>
      locator.evaluate((element) => {
        const rect = element.getBoundingClientRect()
        const hit = document.elementFromPoint(
          rect.left + rect.width / 2,
          rect.top + rect.height / 2,
        )
        return !!hit && element.contains(hit)
      }),
    )
    .toBe(true)
}
test('Home pages share heading geometry, context mark, demo controls, summary cards and queue cards', async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 1508, height: 900 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const geometry = []
  for (const home of pages) {
    await page.goto(`/w/atelier-kyoto/home/${home.id}`)
    await page.evaluate(() => document.fonts.ready.then(() => undefined))
    const heading = page.getByRole('heading', {
      name: home.title,
      exact: true,
      level: 1,
    })
    const context = page.locator('[data-page-context]')
    await expect(context).toHaveText('Atelier Kyoto/Home')
    await expect(context.locator('[aria-hidden="true"]').first()).toBeVisible()
    await expect(page.getByText('DEMO WORKSPACE', { exact: true })).toHaveCount(
      0,
    )
    const demo = page.getByText('Demo data', { exact: true })
    await expect(demo.locator('svg')).toBeVisible()
    const summary =
      home.id === 'overview'
        ? page
            .getByRole('region', { name: 'Store summary' })
            .getByRole('article')
            .first()
        : page.getByRole('tab').first()
    const card = page.locator('[data-resource-id]').first()
    geometry.push({
      heading: await heading.boundingBox(),
      context: await context.boundingBox(),
      demo: await demo.boundingBox(),
      summary: await summary.boundingBox(),
      card: await card.evaluate((element) => {
        const style = getComputedStyle(element)
        const footer = getComputedStyle(element.lastElementChild!)
        return {
          padding: style.padding,
          border: style.borderLeftWidth,
          footerPadding: footer.paddingTop,
          footerMargin: footer.marginTop,
          footerBorder: footer.borderTopWidth,
        }
      }),
    })
    if (home.id === 'my-work') {
      await expect(card.getByText('Product', { exact: true })).toBeVisible()
      await expect(page.getByRole('tab').first().locator('strong')).toHaveText(
        '2',
      )
    }
    await page.screenshot({
      path: testInfo.outputPath(`${home.id}-shared-home.png`),
    })
  }
  const [overview, work] = geometry
  expect(work!.heading!.x).toBe(overview!.heading!.x)
  expect(work!.heading!.y).toBe(overview!.heading!.y)
  expect(work!.heading!.height).toBe(overview!.heading!.height)
  expect(work!.context!.x).toBe(overview!.context!.x)
  expect(work!.context!.y).toBe(overview!.context!.y)
  expect(work!.context!.height).toBe(overview!.context!.height)
  expect(work!.demo).toEqual(overview!.demo)
  expect(work!.summary!.width).toBe(overview!.summary!.width)
  expect(work!.summary!.height).toBe(overview!.summary!.height)
  expect(work!.card).toEqual(overview!.card)
})
for (const home of pages) {
  test(`${home.title} sort control keeps its size when the value changes`, async ({
    page,
  }) => {
    await page.goto(`/w/atelier-kyoto/home/${home.id}`)
    const sort = page.getByRole('button', {
      name: home.id === 'overview' ? /Sort by/ : /Sort work/,
    })
    const before = (await sort.boundingBox())!
    await sort.click()
    await page
      .getByRole('option', {
        name: home.id === 'overview' ? 'Name A–Z' : 'Product name',
        exact: true,
      })
      .click()
    const after = (await sort.boundingBox())!
    expect(after.width).toBe(before.width)
    expect(after.height).toBe(before.height)
  })
  for (const width of [900, 430, 320]) {
    test(`${home.title} confirmation is above the inspector and receives input at ${width}px`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 932 })
      await page.emulateMedia({ reducedMotion: 'reduce' })
      await page.goto(`/w/atelier-kyoto/home/${home.id}`)
      await page
        .getByRole('button', { name: 'View details for Kanso Stone Basin' })
        .click()
      const inspector = page.getByRole('dialog', {
        name: 'Page context',
        exact: true,
      })
      await expect(inspector).toBeVisible()
      await inspector
        .getByRole('button', { name: home.action, exact: true })
        .click()
      const dialog = page.getByRole('dialog', {
        name: home.dialog,
        exact: true,
      })
      const confirm = dialog.getByRole('button', {
        name: home.confirm,
        exact: true,
      })
      await expect(dialog).toBeVisible()
      await receivesPointer(
        dialog.getByRole('heading', { name: home.dialog, exact: true }),
      )
      await receivesPointer(confirm)
      const cancel = dialog.getByRole('button', { name: 'Cancel', exact: true })
      await cancel.focus()
      await page.keyboard.press('Shift+Tab')
      await expect(confirm).toBeFocused()
      await page.keyboard.press('Tab')
      await expect(cancel).toBeFocused()
      await page.screenshot({
        path: testInfo.outputPath(`${home.id}-confirmation-${width}.png`),
      })
      await cancel.click()
      await expect(dialog).toBeHidden()
      await expect(inspector).toBeVisible()
      await expect(
        inspector.getByRole('button', { name: home.action, exact: true }),
      ).toBeFocused()
      await inspector
        .getByRole('button', { name: home.action, exact: true })
        .click()
      await confirm.click()
      await expect(dialog).toBeHidden()
      await expect(
        inspector.getByRole('heading', {
          name: 'Kanso Stone Basin',
          exact: true,
        }),
      ).toBeFocused()
      if (home.id === 'overview')
        await expect(inspector).toContainText('No demo action needed')
      else
        await expect(
          inspector.getByText('Submitted', { exact: true }),
        ).toBeVisible()
    })
  }
}
