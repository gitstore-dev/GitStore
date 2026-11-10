import { storybookURL } from './storybook-url'
import { test, expect } from '@playwright/test'
import type { Page } from '@playwright/test'
const base = '/w/atelier-kyoto/catalog/products'
async function observe(page: Page) {
  await page.evaluate(() => {
    const doc = document as Document & {
      productCaptures?: { before: string[]; after: string[]; error?: string }[]
    }
    doc.productCaptures = []
    const native = doc.startViewTransition.bind(doc)
    doc.startViewTransition = (
      ...args: Parameters<Document['startViewTransition']>
    ) => {
      const capture = document.documentElement.dataset.productTransition
        ? {
            before: Array.from(
              document.querySelectorAll<HTMLElement>('[data-product-motion]'),
            ).map((n) => n.dataset.productMotion!),
            after: [] as string[],
            snapshots: [] as string[],
            imageReady: false,
            error: undefined as string | undefined,
          }
        : undefined
      const transition = native(...args)
      if (capture) {
        doc.productCaptures!.push(capture)
        void transition.ready.then(
          () => {
            capture.imageReady = Array.from(
              document.querySelectorAll<HTMLImageElement>(
                '[data-product-motion="image"] img',
              ),
            ).every((image) => image.complete && image.naturalWidth > 0)
            capture.snapshots = document
              .getAnimations()
              .map(
                (a) => (a.effect as KeyframeEffect | null)?.pseudoElement ?? '',
              )
            capture.after = Array.from(
              document.querySelectorAll<HTMLElement>('[data-product-motion]'),
            ).map((n) => n.dataset.productMotion!)
          },
          (error: Error) => {
            capture.error = error.message
          },
        )
      }
      return transition
    }
  })
}
async function settled(page: Page) {
  await expect(page.locator('html')).not.toHaveAttribute(
    'data-product-transition',
  )
  await expect(page.locator('[data-product-motion]')).toHaveCount(0)
}
for (const [view, width, theme, id, title] of [
  ['table', 1508, 'light', 'stone-basin', 'Kanso Stone Basin'],
  ['grid', 1508, 'dark', 'stone-basin', 'Kanso Stone Basin'],
  ['table', 912, 'light', 'silk-tunic', 'Raw Silk Tunic'],
  ['grid', 375, 'dark', 'stone-basin', 'Kanso Stone Basin'],
] as const) {
  test(`${view} to detail and browser Back share real snapshots at ${width}px in ${theme}`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 850 })
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await page.addInitScript(
      (theme) => localStorage.setItem('gitstore.theme', theme),
      theme,
    )
    await page.goto(`${base}?view=${view}&q=${encodeURIComponent(title)}`)
    await observe(page)
    const item = page.locator(`[data-product-surface="${id}"]`)
    await item.focus()
    const y = await page.evaluate(() => scrollY)
    await item.press('Enter')
    await expect(
      page.getByRole('heading', { level: 1, name: title, exact: true }),
    ).toBeFocused()
    await settled(page)
    await expect
      .poll(() =>
        page.evaluate(
          () =>
            (document as Document & { productCaptures?: unknown[] })
              .productCaptures?.length,
        ),
      )
      .toBe(1)
    const captures = await page.evaluate(
      () =>
        (
          document as Document & {
            productCaptures?: {
              before: string[]
              after: string[]
              snapshots: string[]
              imageReady: boolean
              error?: string
            }[]
          }
        ).productCaptures,
    )
    expect(captures![0]!.error).toBeUndefined()
    expect(captures![0]!.imageReady).toBe(true)
    expect(captures![0]!.snapshots).toEqual(
      expect.arrayContaining([
        '::view-transition-old(gs-product-title)',
        '::view-transition-new(gs-product-title)',
        '::view-transition-old(gs-product-image)',
        '::view-transition-new(gs-product-image)',
      ]),
    )
    expect(captures![0]!.before.toSorted()).toEqual([
      'image',
      'row-content',
      'surface',
      'title',
    ])
    expect(captures![0]!.after.toSorted()).toEqual([
      'detail-content',
      'image',
      'surface',
      'title',
    ])
    await page.screenshot({
      path: testInfo.outputPath(`product-expanded-${theme}-${width}.png`),
      fullPage: true,
    })
    await page.goBack()
    await expect(page.locator('[data-view]')).toHaveAttribute('data-view', view)
    await settled(page)
    await expect(item).toBeFocused()
    expect(
      Math.abs((await page.evaluate(() => scrollY)) - y),
    ).toBeLessThanOrEqual(2)
    await expect(
      page.getByRole('searchbox', { name: 'Search products' }),
    ).toHaveValue(title)
    const returned = await page.evaluate(
      () =>
        (document as Document & { productCaptures?: { error?: string }[] })
          .productCaptures,
    )
    expect(returned).toHaveLength(2)
    expect(returned![1]!.error).toBeUndefined()
    await page.screenshot({
      path: testInfo.outputPath(`product-returned-${theme}-${width}.png`),
    })
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
  })
}
for (const fallback of [
  'system-reduced',
  'preference-reduced',
  'unsupported',
] as const)
  test(`${fallback} navigates and restores keyboard focus without snapshots`, async ({
    page,
  }) => {
    await page.emulateMedia({
      reducedMotion: fallback === 'system-reduced' ? 'reduce' : 'no-preference',
    })
    if (fallback === 'preference-reduced')
      await page.addInitScript(() =>
        localStorage.setItem('gitstore.motion', 'reduced'),
      )
    if (fallback === 'unsupported')
      await page.addInitScript(() =>
        Object.defineProperty(document, 'startViewTransition', {
          value: undefined,
          configurable: true,
        }),
      )
    await page.goto(base)
    const item = page.locator('[data-product-surface="stone-basin"]')
    await item.click()
    await expect(
      page.getByRole('heading', { name: 'Kanso Stone Basin', level: 1 }),
    ).toBeFocused()
    await settled(page)
    await page
      .getByRole('main')
      .getByRole('link', { name: 'Products', exact: true })
      .click()
    await expect(item).toBeFocused()
    await settled(page)
  })

test('return preserves loaded batches and list scroll; Focus interruption and rapid history leave no stale layers', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.goto(base)
  for (let index = 1; index <= 5; index++) {
    await page.getByRole('link', { name: 'New product', exact: true }).click()
    await page
      .getByRole('textbox', { name: 'Title', exact: true })
      .fill(`Motion example ${index}`)
    await page
      .getByRole('button', { name: 'Create demo product', exact: true })
      .click()
    await page
      .getByRole('main')
      .getByRole('link', { name: 'Products', exact: true })
      .click()
  }
  await page.getByRole('button', { name: 'Load more products' }).click()
  const last = page.locator('[data-product-surface]').last()
  const id = await last.getAttribute('data-product-surface')
  await last.scrollIntoViewIfNeeded()
  const scroll = await page.evaluate(() => scrollY)
  await last.click()
  await settled(page)
  await page
    .getByRole('main')
    .getByRole('link', { name: 'Products', exact: true })
    .click()
  await settled(page)
  await expect(page.locator('[data-product-surface]')).toHaveCount(9)
  await expect(page.locator(`[data-product-surface="${id}"]`)).toBeFocused()
  expect(
    Math.abs((await page.evaluate(() => scrollY)) - scroll),
  ).toBeLessThanOrEqual(2)
  await last.click()
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await settled(page)
  await expect(page.locator('html')).not.toHaveAttribute(
    'data-focus-transition',
  )
  await page.goBack()
  await page.goForward()
  await page.goBack()
  await settled(page)
  await expect(page.locator(`[data-product-surface="${id}"]`)).toBeVisible()
  await expect(page.locator('[data-grid-exit]')).toHaveCount(0)
})

for (const direction of ['open', 'return'] as const)
  test(`${direction} mid-transition keeps a continuous surface and anchored shell`, async ({
    page,
  }, testInfo) => {
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await page.goto(`${base}?view=grid`)
    if (direction === 'return') {
      await page.locator('[data-product-surface="stone-basin"]').click()
      await expect(
        page.getByRole('heading', { name: 'Kanso Stone Basin', level: 1 }),
      ).toBeVisible()
      await settled(page)
    }
    await page.evaluate(() => {
      const native = document.startViewTransition.bind(document)
      document.startViewTransition = (
        ...args: Parameters<Document['startViewTransition']>
      ) => {
        const transition = native(...args)
        void transition.ready.then(() => {
          for (const animation of document.getAnimations()) {
            if (
              (
                animation.effect as KeyframeEffect | null
              )?.pseudoElement?.startsWith('::view-transition')
            ) {
              animation.pause()
              animation.currentTime = 150
            }
          }
        })
        return transition
      }
    })
    if (direction === 'open')
      await page.locator('[data-product-surface="stone-basin"]').click()
    else await page.goBack()
    await expect(page.locator('html')).toHaveAttribute(
      'data-product-transition',
      direction,
    )
    await expect
      .poll(() =>
        page.evaluate(
          () =>
            document
              .getAnimations()
              .filter(
                (a) =>
                  (
                    a.effect as KeyframeEffect | null
                  )?.pseudoElement?.startsWith('::view-transition') &&
                  a.playState === 'paused',
              ).length,
        ),
      )
      .toBeGreaterThan(0)
    const geometry = await page.evaluate(() => {
      const html = document.documentElement
      return {
        surface: getComputedStyle(
          html,
          '::view-transition-group(gs-product-surface)',
        ).width,
        header: getComputedStyle(
          html,
          '::view-transition-group(gs-product-header)',
        ).transform,
      }
    })
    expect(parseFloat(geometry.surface)).toBeGreaterThan(200)
    expect(geometry.header).not.toBe('none')
    const layoutDuring = await page
      .locator('[data-product-title], [data-product-image]')
      .evaluateAll((nodes) =>
        nodes.map((node) => {
          const r = node.getBoundingClientRect()
          return { x: r.x, y: r.y, width: r.width, height: r.height }
        }),
      )
    await page.screenshot({
      path: testInfo.outputPath(`product-${direction}-midpoint.png`),
    })
    await page.evaluate(() =>
      document.getAnimations().forEach((a) => {
        if (a.playState === 'paused') a.finish()
      }),
    )
    await settled(page)
    const layoutAfter = await page
      .locator('[data-product-title], [data-product-image]')
      .evaluateAll((nodes) =>
        nodes.map((node) => {
          const r = node.getBoundingClientRect()
          return { x: r.x, y: r.y, width: r.width, height: r.height }
        }),
      )
    expect(layoutAfter).toEqual(layoutDuring)
  })

for (const story of [
  'table-expansion',
  'grid-expansion-dark',
  'mobile-expansion',
  'reduced-motion-expansion',
])
  test(`Product ${story} story completes the round trip`, async ({ page }) => {
    await page.goto(
      `${storybookURL}/?path=/story/pages-catalog-products--${story}`,
    )
    await page.getByRole('tab', { name: /^Interactions/ }).click()
    await expect(
      page
        .getByRole('toolbar', { name: 'Component test playback controls' })
        .getByText(/^pass$/i),
    ).toBeVisible({ timeout: 20000 })
  })

for (const view of ['grid', 'table'] as const)
  for (const direction of ['open', 'return'] as const)
    test(`${view} ${direction} transition keeps visible content throughout every sampled frame`, async ({
      page,
    }) => {
      await page.emulateMedia({ reducedMotion: 'no-preference' })
      await page.goto(`${base}?view=${view}`)
      if (direction === 'return') {
        await page.locator('[data-product-surface="stone-basin"]').click()
        await expect(
          page.getByRole('heading', { name: 'Kanso Stone Basin', level: 1 }),
        ).toBeVisible()
        await settled(page)
      }
      await page.evaluate(() => {
        const state = window as typeof window & {
          productContinuity?: {
            root: number
            surface: number
            title: number
            image: number
          }[]
          productFinished?: boolean
        }
        state.productContinuity = []
        const start = document.startViewTransition.bind(document)
        document.startViewTransition = (
          ...args: Parameters<Document['startViewTransition']>
        ) => {
          const transition = start(...args)
          if (document.documentElement.dataset.productTransition) {
            state.productFinished = false
            void transition.ready.then(() => {
              const sample = () => {
                if (state.productFinished) return
                const sum = (name: string) => {
                  const old = getComputedStyle(
                    document.documentElement,
                    `::view-transition-old(${name})`,
                  )
                  const next = getComputedStyle(
                    document.documentElement,
                    `::view-transition-new(${name})`,
                  )
                  const a = Number(old.opacity),
                    b = Number(next.opacity)
                  return next.mixBlendMode === 'plus-lighter'
                    ? Math.min(1, a + b)
                    : 1 - (1 - a) * (1 - b)
                }
                state.productContinuity!.push({
                  root: sum('root'),
                  surface: sum('gs-product-surface'),
                  title: sum('gs-product-title'),
                  image: sum('gs-product-image'),
                })
                requestAnimationFrame(sample)
              }
              requestAnimationFrame(sample)
            })
            void transition.finished.finally(() => {
              state.productFinished = true
            })
          }
          return transition
        }
      })
      if (direction === 'open') {
        await page.locator('[data-product-surface="stone-basin"]').click()
        await expect(
          page.getByRole('heading', { name: 'Kanso Stone Basin', level: 1 }),
        ).toBeVisible()
      } else await page.goBack()
      await expect
        .poll(() =>
          page.evaluate(
            () =>
              (window as typeof window & { productFinished?: boolean })
                .productFinished,
          ),
        )
        .toBe(true)
      const samples = await page.evaluate(
        () =>
          (
            window as typeof window & {
              productContinuity?: {
                root: number
                surface: number
                title: number
                image: number
              }[]
            }
          ).productContinuity!,
      )
      expect(samples.length).toBeGreaterThan(3)
      for (const part of ['root', 'surface', 'title', 'image'] as const)
        expect(
          Math.min(...samples.map((s) => s[part])),
          `${part} must not disappear between outgoing and incoming snapshots`,
        ).toBeGreaterThanOrEqual(0.98)
    })
