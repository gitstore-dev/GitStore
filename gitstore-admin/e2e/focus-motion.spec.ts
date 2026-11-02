import { test, expect } from '@playwright/test'

test('focus wipes the shell and reverses while preserving working DOM, selection, and scroll', async ({
  page,
}, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.goto('/w/atelier-kyoto/home/overview')
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
  const main = page.locator('main')
  await main.evaluate((element) => {
    element.dataset.identity = 'persistent-working-page'
    element.style.minHeight = '2000px'
    const field = document.createElement('input')
    field.setAttribute('aria-label', 'Working draft fixture')
    field.value = 'Unsaved working draft'
    field.setSelectionRange(2, 8)
    element.append(field)
    window.scrollTo(0, 240)
  })
  const node = await main.elementHandle()
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await expect(page.locator('html')).toHaveAttribute(
    'data-focus-transition',
    'enter',
  )
  await page.waitForFunction(() =>
    document
      .getAnimations()
      .some((a) => a.effect?.getTiming().duration === 420),
  )
  await page.evaluate(() => {
    for (const animation of document.getAnimations()) {
      animation.pause()
      animation.currentTime = 150
    }
  })
  await page.screenshot({
    path: testInfo.outputPath('focus-enter-midpoint.png'),
  })
  await page.evaluate(() =>
    document.getAnimations().forEach((animation) => animation.play()),
  )
  await expect(page.locator('html')).not.toHaveAttribute(
    'data-focus-transition',
  )
  await expect(
    page.getByRole('button', { name: 'Exit Focus Mode' }),
  ).toBeFocused()
  expect(await node!.evaluate((element) => element.isConnected)).toBe(true)
  expect(await page.evaluate(() => scrollY)).toBe(240)
  const field = page.getByRole('textbox', { name: 'Working draft fixture' })
  await expect(field).toHaveValue('Unsaved working draft')
  expect(
    await field.evaluate((element) => [
      (element as HTMLInputElement).selectionStart,
      (element as HTMLInputElement).selectionEnd,
    ]),
  ).toEqual([2, 8])
  await page.getByRole('button', { name: 'Exit Focus Mode' }).click()
  await expect(page.locator('html')).not.toHaveAttribute(
    'data-focus-transition',
  )
  await expect(
    page.getByRole('button', { name: 'Focus mode', exact: true }),
  ).toBeFocused()
  expect(await node!.evaluate((element) => element.isConnected)).toBe(true)
  await expect(page.locator('[data-focus-gpu]')).toHaveCount(0)
})

for (const source of ['system', 'application'] as const) {
  test(`${source} reduced motion skips snapshots and GPU work`, async ({
    page,
  }) => {
    await page.emulateMedia({
      reducedMotion: source === 'system' ? 'reduce' : 'no-preference',
    })
    if (source === 'application')
      await page.addInitScript(() =>
        localStorage.setItem('gitstore.motion', 'reduced'),
      )
    await page.addInitScript(() => {
      document.startViewTransition = () => {
        throw new Error('Reduced motion must not capture snapshots')
      }
    })
    await page.goto('/w/atelier-kyoto/home/overview')
    await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
    await expect(
      page.getByRole('button', { name: 'Exit Focus Mode' }),
    ).toBeFocused()
    await expect(page.locator('html')).not.toHaveAttribute(
      'data-focus-transition',
    )
    await expect(page.locator('[data-focus-gpu]')).toHaveCount(0)
    expect(await page.evaluate(() => document.getAnimations().length)).toBe(0)
  })
}

test('missing View Transitions and rapid reversals leave a usable shell', async ({
  page,
}) => {
  await page.addInitScript(() =>
    Object.defineProperty(document, 'startViewTransition', {
      configurable: true,
      value: undefined,
    }),
  )
  await page.goto('/w/atelier-kyoto/home/overview')
  for (let cycle = 0; cycle < 2; cycle++) {
    await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
    await page.getByRole('button', { name: 'Exit Focus Mode' }).click()
  }
  await expect(
    page.getByRole('button', { name: 'Focus mode', exact: true }),
  ).toBeFocused()
  await expect(
    page.getByRole('navigation', { name: 'Main navigation', exact: true }),
  ).toBeVisible()
  await expect(page.locator('[data-focus-gpu]')).toHaveCount(0)
  await expect
    .poll(() => page.evaluate(() => document.getAnimations().length))
    .toBe(0)
})

test('native snapshots can be interrupted and reversed without stale layers', async ({
  page,
}, testInfo) => {
  await page.goto('/w/atelier-kyoto/home/overview')
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await page.getByRole('button', { name: 'Exit Focus Mode' }).click()
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await expect(
    page.getByRole('button', { name: 'Exit Focus Mode' }),
  ).toBeFocused()
  await expect(page.locator('html')).not.toHaveAttribute(
    'data-focus-transition',
  )
  await expect(page.locator('[data-focus-gpu]')).toHaveCount(0)
  await page.screenshot({ path: testInfo.outputPath('focus-mode.png') })
  await page.getByRole('button', { name: 'Exit Focus Mode' }).click()
  await expect(page.locator('html')).not.toHaveAttribute(
    'data-focus-transition',
  )
  await expect(
    page.getByRole('navigation', { name: 'Main navigation', exact: true }),
  ).toBeVisible()
})
