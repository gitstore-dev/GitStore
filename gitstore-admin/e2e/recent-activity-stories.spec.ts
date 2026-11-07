import { storybookURL } from './storybook-url'
import { expect, test } from '@playwright/test'
for (const width of [981, 1200, 1440]) {
  for (const story of [
    'historical-preview',
    'recovery-confirmation',
    'completed-recovery',
    'preserved-draft-buffer',
    'recovery-error',
    'mobile-recovery',
  ]) {
    test(`Recent activity ${story} story completes its interaction at ${width}px`, async ({
      page,
    }) => {
      await page.setViewportSize({ width, height: 763 })
      await page.goto(
        `${storybookURL}/?path=/story/pages-home-recent-activity--${story}`,
      )
      const canvas = page.frameLocator('#storybook-preview-iframe')
      if (story === 'recovery-confirmation' || story === 'mobile-recovery')
        await expect(
          canvas.getByRole('dialog', { name: 'Restore this version in demo?' }),
        ).toBeVisible({ timeout: 15_000 })
      else if (story === 'preserved-draft-buffer')
        await expect(
          canvas.getByRole('textbox', { name: 'Title', exact: true }),
        ).toHaveValue('Kanso Stone Basin — Unsaved', { timeout: 15_000 })
      else if (story === 'completed-recovery')
        await expect(
          canvas.getByText('Restored in demo', { exact: true }).last(),
        ).toBeVisible({ timeout: 15_000 })
      else if (story === 'recovery-error')
        await expect(
          canvas.getByRole('button', {
            name: 'Restore this version in demo',
            exact: true,
          }),
        ).toBeDisabled({ timeout: 15_000 })
      else
        await expect(
          canvas.getByRole('region', { name: 'Recorded changes' }),
        ).toBeVisible({ timeout: 15_000 })
      await page.getByRole('tab', { name: /^Interactions\b/ }).click()
      await expect(
        page
          .getByRole('toolbar', { name: 'Component test playback controls' })
          .getByText(/^pass$/i),
      ).toBeVisible()
    })
  }
}
test('activity state specimens and demo retry are isolated', async ({
  page,
}) => {
  for (const [story, heading] of [
    ['empty-store', 'No activity yet'],
    ['loading', 'Loading activity'],
    ['unavailable', 'Activity is unavailable'],
    ['restricted', 'Activity is restricted in this preview'],
    ['no-results', 'No matching activity'],
  ]) {
    await page.goto(
      `${storybookURL}/iframe.html?id=pages-home-recent-activity--${story}&viewMode=story`,
    )
    await expect(
      page.getByRole('heading', { name: heading, exact: true }),
    ).toBeVisible()
    if (story === 'unavailable') {
      await page.getByRole('button', { name: 'Retry demo' }).click()
      await expect(
        page.getByRole('searchbox', { name: 'Search activity' }),
      ).toBeVisible()
    }
  }
})
