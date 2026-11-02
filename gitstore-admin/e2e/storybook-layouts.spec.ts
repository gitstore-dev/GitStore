import { expect, test } from '@playwright/test'

for (const story of ['mobile-drawer-open', 'mobile-profile-menu-open']) {
  test(`Storybook ${story} opens its mobile overlays through the play function`, async ({
    page,
  }, testInfo) => {
    await page.goto(
      `http://127.0.0.1:6006/?path=/story/foundation-layouts--${story}`,
    )
    const canvas = page.frameLocator('#storybook-preview-iframe')
    // Use the actual manager viewport and story play function; no test clicks
    // should compensate for a broken interaction in the story itself.
    await expect(
      canvas.getByRole('dialog', { name: 'Mobile navigation', exact: true }),
    ).toBeVisible({ timeout: 15_000 })
    if (story === 'mobile-profile-menu-open') {
      const menu = canvas.getByRole('menu')
      await expect(menu).toBeVisible()
      await expect(
        menu.getByRole('menuitem', {
          name: 'Profile & preferences',
          exact: true,
        }),
      ).toBeVisible()
    }
    await page.getByRole('tab', { name: /^Interactions\b/ }).click()
    await expect(
      page
        .getByRole('toolbar', { name: 'Component test playback controls' })
        .getByText(/^pass$/i),
    ).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`${story}.png`) })
  })
}
