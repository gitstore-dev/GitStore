import { expect, test } from '@playwright/test'
const home = '/w/atelier-kyoto/home/overview'

test('filter choreography preserves live cards, focus, and inert exits, then cleans up', async ({
  page,
}, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  // Hold only grid animations so geometry assertions do not depend on machine speed.
  await page.addInitScript(() => {
    const animate = Element.prototype.animate
    Element.prototype.animate = function (frames, options) {
      const animation = animate.call(this, frames, options)
      if (
        this.matches('[data-resource-id], [data-grid-exit], [data-grid-moving]')
      ) {
        animation.pause()
        animation.currentTime = 0
      }
      return animation
    }
  })
  await page.goto(home)
  const live = page
    .locator('[data-resource-id]')
    .filter({ hasText: 'lookbook-hero.webp' })
  const identity = await live.elementHandle()
  const before = await live.boundingBox()
  const filter = page.getByRole('radio', { name: /Processing/ })
  await filter.press('Space')
  await expect(page.locator('[data-resource-id]')).toHaveCount(2)
  await expect(filter).toBeFocused()
  await expect(page.locator('[data-grid-exit]')).toHaveCount(4)
  expect(await identity!.evaluate((node) => node.isConnected)).toBe(true)
  const held = await live.boundingBox()
  expect(held!.y).toBeCloseTo(before!.y, 0)
  expect(
    await page
      .locator('[data-grid-exit]')
      .evaluateAll((nodes) =>
        nodes.every(
          (node) =>
            (node as HTMLElement).inert &&
            node.getAttribute('aria-hidden') === 'true' &&
            !node.hasAttribute('data-resource-id'),
        ),
      ),
  ).toBe(true)
  await page.evaluate(() =>
    document.getAnimations().forEach((animation) => {
      animation.currentTime = 90
    }),
  )
  await page.screenshot({
    path: testInfo.outputPath('overview-filter-midpoint.png'),
    fullPage: true,
  })
  await page.evaluate(() =>
    document.getAnimations().forEach((animation) => animation.finish()),
  )
  await expect(page.locator('[data-grid-exit]')).toHaveCount(0)
  await expect(page.locator('[data-grid-moving]')).toHaveCount(0)
  expect((await live.boundingBox())!.y).toBeLessThan(before!.y)
  await expect(filter).toBeFocused()
  await expect(
    page.getByText('Made for considered commerce.', { exact: true }),
  ).toHaveCount(0)
})

test('rapid filter/sort/reset and Focus interruption leave no stale animations or copies', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.goto(home)
  for (const label of [
    /Has errors/,
    /Needs review/,
    /Out of date/,
    /Processing/,
    /^All /,
  ]) {
    await page.getByRole('radio', { name: label }).press('Space')
  }
  await page.getByRole('button', { name: /Sort by/ }).click()
  await page.getByRole('option', { name: 'Name A–Z' }).click()
  await expect(
    page.getByRole('button', { name: /^View details for / }).first(),
  ).toHaveAccessibleName('View details for Autumn Objects')
  await page.getByRole('button', { name: 'Reset demo' }).click()
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await expect(
    page.getByRole('button', { name: 'Exit Focus Mode' }),
  ).toBeFocused()
  await expect(
    page.locator('[data-grid-exit], [data-grid-moving]'),
  ).toHaveCount(0)
  await page.getByRole('button', { name: 'Exit Focus Mode' }).click()
  await expect(
    page.getByRole('button', { name: 'Focus mode', exact: true }),
  ).toBeFocused()
  await expect(page.locator('[data-resource-id]')).toHaveCount(6)
  await page.getByRole('link', { name: 'My work', exact: true }).click()
  await expect(
    page.locator('[data-resource-lighting], [data-grid-exit]'),
  ).toHaveCount(0)
})

for (const source of ['system', 'application']) {
  test(`${source} reduced motion uses immediate reflow and CSS lighting`, async ({
    page,
  }) => {
    await page.emulateMedia({
      reducedMotion: source === 'system' ? 'reduce' : 'no-preference',
    })
    if (source === 'application')
      await page.addInitScript(() =>
        localStorage.setItem('gitstore.motion', 'reduced'),
      )
    await page.goto(home)
    await page.getByRole('radio', { name: /Needs review/ }).press('Space')
    await expect(page.locator('[data-resource-id]')).toHaveCount(2)
    await expect(
      page.locator(
        '[data-grid-exit], [data-grid-moving], [data-resource-lighting]',
      ),
    ).toHaveCount(0)
    expect(await page.evaluate(() => document.getAnimations().length)).toBe(0)
  })
}

test('CSS-only Storybook lighting and filtering keep the same controls and states', async ({
  page,
}) => {
  await page.goto(
    'http://127.0.0.1:6006/iframe.html?id=pages-home-overview--css-state-lighting&viewMode=story',
  )
  await expect(page.locator('[data-lighting="css"]')).toBeVisible()
  await expect(page.locator('[data-resource-lighting]')).toHaveCount(0)
  await page.getByRole('radio', { name: /Needs review/ }).press('Space')
  await expect(page.locator('[data-resource-id]')).toHaveCount(2)
  await expect(
    page.getByRole('button', { name: 'View details for Kanso Stone Basin' }),
  ).toBeVisible()
  await expect(page.locator('[data-grid-exit]')).toHaveCount(0)
})
