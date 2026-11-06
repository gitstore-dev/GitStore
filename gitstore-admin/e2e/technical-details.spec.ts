import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
for (const width of [1440, 390]) {
  for (const theme of ['light', 'dark'] as const) {
    test(`inspectors share keyboard-accessible Technical details at ${width}px in ${theme}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 1000 })
      await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme })
      const styles = []
      for (const destination of ['overview', 'my-work']) {
        await page.goto(`/w/atelier-kyoto/home/${destination}`)
        await page
          .getByRole('button', { name: 'View details for Kanso Stone Basin' })
          .click()
        const inspector =
          width > 1100
            ? page.getByRole('complementary', { name: 'Page context' })
            : page.getByRole('dialog', { name: 'Page context', exact: true })
        const details = inspector.locator('details[data-technical-details]')
        const summary = details.locator('summary')
        await expect(summary).toHaveText('Technical details')
        await expect(details.locator('code')).toBeHidden()
        await summary.focus()
        await page.keyboard.press('Enter')
        await expect(details).toHaveJSProperty('open', true)
        await expect(
          details.getByText('Resource type', { exact: true }),
        ).toBeVisible()
        await expect(details.locator('dd')).toHaveText('Product')
        await expect(
          details.getByText('Source file', { exact: true }),
        ).toBeVisible()
        await expect(details.locator('svg')).toBeVisible()
        await expect(details.locator('code')).toHaveText(
          'catalog/products/kanso-stone-basin.md',
        )
        const path = (await details.locator('code').boundingBox())!
        expect(path.x).toBeGreaterThanOrEqual(0)
        expect(path.x + path.width).toBeLessThanOrEqual(width)
        styles.push(
          await details.evaluate((element) => {
            const disclosure = getComputedStyle(element)
            const summary = getComputedStyle(element.querySelector('summary')!)
            const code = getComputedStyle(element.querySelector('code')!)
            return {
              font: disclosure.font,
              padding: disclosure.paddingTop,
              margin: disclosure.marginTop,
              border: disclosure.borderTop,
              summaryColor: summary.color,
              outline: summary.outline,
              codeFont: code.font,
              codeColor: code.color,
            }
          }),
        )
        expect(
          (
            await new AxeBuilder({ page })
              .include('details[data-technical-details]')
              .analyze()
          ).violations,
        ).toEqual([])
        await page.screenshot({
          path: testInfo.outputPath(
            `${destination}-technical-details-${width}-${theme}.png`,
          ),
        })
        await summary.press('Space')
        await expect(details).toHaveJSProperty('open', false)
        await expect(summary).toBeFocused()
      }
      expect(styles[1]).toEqual(styles[0])
    })
  }
}
