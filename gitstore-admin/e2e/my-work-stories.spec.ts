import { expect, test } from '@playwright/test'
for (const story of [
  'change-preview',
  'editing',
  'unsaved-changes',
  'unsaved-confirmation',
  'submit-confirmation',
  'review-confirmation',
]) {
  test(`My work ${story} story completes its interaction`, async ({ page }) => {
    await page.goto(
      `http://127.0.0.1:6006/?path=/story/pages-home-my-work--${story}`,
    )
    const canvas = page.frameLocator('#storybook-preview-iframe')
    if (story === 'unsaved-confirmation')
      await expect(
        canvas.getByRole('dialog', { name: 'Discard unsaved edits?' }),
      ).toBeVisible()
    else if (story === 'submit-confirmation')
      await expect(
        canvas.getByRole('dialog', { name: 'Submit demo draft?' }),
      ).toBeVisible()
    else if (story === 'review-confirmation')
      await expect(
        canvas.getByRole('textbox', { name: 'Review note' }),
      ).toBeVisible()
    else if (story === 'editing' || story === 'unsaved-changes')
      await expect(
        canvas.getByRole('textbox', { name: 'Product name', exact: true }),
      ).toBeVisible()
    else
      await expect(
        canvas.getByRole('region', { name: 'Change preview' }),
      ).toBeVisible()
    await page.getByRole('tab', { name: /^Interactions\b/ }).click()
    await expect(
      page
        .getByRole('toolbar', { name: 'Component test playback controls' })
        .getByText(/^pass$/i),
    ).toBeVisible()
  })
}
test('My work unavailable and read-only stories expose presentation states without workflow writes', async ({
  page,
}) => {
  await page.goto(
    'http://127.0.0.1:6006/iframe.html?id=pages-home-my-work--unavailable&viewMode=story',
  )
  await expect(
    page.getByRole('heading', { name: 'My work is unavailable' }),
  ).toBeVisible()
  await page.getByRole('button', { name: 'Retry demo' }).click()
  await expect(
    page.getByRole('tablist', { name: 'My work views' }),
  ).toBeVisible()
  await page.goto(
    'http://127.0.0.1:6006/iframe.html?id=pages-home-my-work--read-only&viewMode=story',
  )
  await page
    .getByRole('button', { name: 'View details for Kanso Stone Basin' })
    .click()
  await expect(
    page.getByRole('region', { name: 'Change preview' }),
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Edit demo draft', exact: true }),
  ).toHaveCount(0)
  await expect(
    page.getByRole('button', { name: 'Submit demo draft' }),
  ).toHaveCount(0)
})
