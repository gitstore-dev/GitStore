import { openDemoReset } from './home-interactions'
import { test, expect } from '@playwright/test'
import type { Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
const path = '/w/atelier-kyoto/home/my-work'
const draftName = 'Kanso Stone Basin'
const tab = (page: Page, name: string) =>
  page.getByRole('tab', { name: new RegExp(`^${name} \\d+\\b`) })
async function openDraft(page: Page) {
  await page
    .getByRole('button', { name: `View details for ${draftName}`, exact: true })
    .click()
  await expect(
    page.getByRole('button', { name: 'Edit demo draft', exact: true }),
  ).toBeVisible()
}
async function editDraft(page: Page) {
  await openDraft(page)
  await page
    .getByRole('button', { name: 'Edit demo draft', exact: true })
    .click()
  await expect(
    page.getByRole('textbox', { name: 'Title', exact: true }),
  ).toBeFocused()
}
async function confirm(page: Page, name: string) {
  const dialog = page.getByRole('dialog', { name, exact: true })
  await dialog.getByRole('button', { name: 'Confirm demo action' }).click()
  await expect(dialog).toBeHidden()
}
test('My work counts, search, tab keyboard navigation and sorting use local fixtures', async ({
  page,
}) => {
  const requests: string[] = []
  page.on('request', (request) => {
    if (/graphql|\/api\//i.test(request.url())) requests.push(request.url())
  })
  await page.goto(path)
  for (const [name, count] of [
    ['Drafts', 2],
    ['Submitted', 2],
    ['Assigned to me', 2],
    ['Completed', 1],
  ] as const)
    await expect(tab(page, name)).toHaveAccessibleName(
      new RegExp(`^${name} ${count}\\b`),
    )
  const search = page.getByRole('searchbox', { name: 'Search my work' })
  await search.fill('silk')
  await expect(page.getByRole('tabpanel').getByRole('article')).toHaveCount(1)
  await expect(tab(page, 'Drafts')).toHaveAccessibleName(/^Drafts 2\b/)
  await search.fill('not a product')
  await expect(
    page.getByRole('heading', { name: 'No matching results' }),
  ).toBeVisible()
  await page
    .getByRole('button', { name: 'Clear search', exact: true })
    .last()
    .click()
  await expect(page.getByRole('tabpanel').getByRole('article')).toHaveCount(2)
  await page.getByRole('button', { name: /Sort work/ }).click()
  await page.getByRole('option', { name: 'Title', exact: true }).click()
  await expect(
    page.getByRole('tabpanel').getByRole('heading', { level: 2 }).first(),
  ).toHaveText(draftName)
  await tab(page, 'Drafts').focus()
  await page.keyboard.press('ArrowRight')
  await expect(tab(page, 'Submitted')).toBeFocused()
  await expect(tab(page, 'Drafts')).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
  await expect(tab(page, 'Submitted')).toHaveAttribute('aria-selected', 'true')
  expect(requests).toEqual([])
})
test('draft editing validates, cancels, saves, previews and submits with confirmation', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto(path)
  await editDraft(page)
  const name = page.getByRole('textbox', { name: 'Title', exact: true })
  await name.fill(' ')
  await expect(page.getByText('Enter a title.')).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Save demo draft' }),
  ).toBeDisabled()
  await name.fill('Kanso Stone Basin — Large')
  await page.getByRole('textbox', { name: 'Description', exact: true }).fill('')
  await expect(
    page.getByRole('button', { name: 'Submit demo draft' }),
  ).toBeDisabled()
  await page.getByRole('button', { name: 'Cancel editing' }).click()
  await page
    .getByRole('dialog', { name: 'Discard unsaved edits?' })
    .getByRole('button', { name: 'Discard unsaved edits', exact: true })
    .click()
  await expect(
    page.getByRole('heading', { name: draftName, exact: true }).last(),
  ).toBeFocused()
  await page
    .getByRole('button', { name: 'Edit demo draft', exact: true })
    .click()
  await name.fill('Kanso Stone Basin — Large')
  await page.getByRole('textbox', { name: 'Description', exact: true }).fill('')
  await page.getByRole('button', { name: 'Save demo draft' }).click()
  await expect(page.getByRole('status')).toContainText('Demo draft saved')
  await expect(
    page
      .getByRole('heading', { name: 'Kanso Stone Basin — Large', exact: true })
      .last(),
  ).toBeFocused()
  await expect(
    page.getByRole('region', { name: 'Title change' }),
  ).toContainText('BeforeKanso Stone BasinAfterKanso Stone Basin — Large')
  await page.getByRole('button', { name: 'Submit demo draft' }).click()
  const dialog = page.getByRole('dialog', { name: 'Submit demo draft?' })
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(
    page.getByRole('button', { name: 'Submit demo draft' }),
  ).toBeFocused()
  await expect(tab(page, 'Submitted')).toHaveAccessibleName(/^Submitted 2\b/)
  await page.getByRole('button', { name: 'Submit demo draft' }).click()
  await confirm(page, 'Submit demo draft?')
  await expect(tab(page, 'Drafts')).toHaveAccessibleName(/^Drafts 1\b/)
  await expect(tab(page, 'Submitted')).toHaveAccessibleName(/^Submitted 3\b/)
  await tab(page, 'Submitted').click()
  await expect(
    page.getByRole('button', {
      name: 'View details for Kanso Stone Basin — Large',
    }),
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Edit demo draft', exact: true }),
  ).toHaveCount(0)
})
test('dirty edits survive canceled tab, selection, close and reset actions', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto(path)
  await editDraft(page)
  await page
    .getByRole('textbox', { name: 'Title', exact: true })
    .fill('Unsaved basin')
  for (const trigger of [
    tab(page, 'Submitted'),
    page.getByRole('button', { name: 'View details for Raw Silk Tunic' }),
    page.getByRole('button', { name: 'Close inspector' }),
  ]) {
    await trigger.click()
    await page
      .getByRole('dialog', { name: 'Discard unsaved edits?' })
      .getByRole('button', { name: 'Cancel', exact: true })
      .click()
    await expect(
      page.getByRole('textbox', { name: 'Title', exact: true }),
    ).toHaveValue('Unsaved basin')
    await expect(tab(page, 'Drafts')).toHaveAttribute('aria-selected', 'true')
  }
  await openDemoReset(page)
  await page
    .getByRole('dialog', {
      name: 'Reset catalog and Home demo for Atelier Kyoto?',
    })
    .getByRole('button', { name: 'Cancel', exact: true })
    .click()
  await expect(
    page.getByRole('textbox', { name: 'Title', exact: true }),
  ).toHaveValue('Unsaved basin')
  await page.getByRole('button', { name: 'Close inspector' }).click()
  await page
    .getByRole('dialog', { name: 'Discard unsaved edits?' })
    .getByRole('button', { name: 'Discard unsaved edits', exact: true })
    .click()
  await expect(
    page.getByRole('complementary', { name: 'Page context' }),
  ).toBeHidden()
  await expect(
    page.getByRole('button', { name: `View details for ${draftName}` }),
  ).toBeFocused()
  await openDraft(page)
  await expect(
    page.getByRole('textbox', { name: 'Title', exact: true }),
  ).toHaveCount(0)
})
test('discard, approve and request changes update the proper lists and retain review outcomes', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto(path)
  await openDraft(page)
  await page.getByRole('button', { name: 'Discard demo draft' }).click()
  await confirm(page, 'Discard demo draft?')
  await expect(tab(page, 'Drafts')).toHaveAccessibleName(/^Drafts 1\b/)
  await tab(page, 'Assigned to me').click()
  await page
    .getByRole('button', { name: `View details for ${draftName}` })
    .click()
  await page.getByRole('button', { name: 'Approve demo changes' }).click()
  await page
    .getByRole('dialog', { name: 'Approve demo changes?' })
    .getByRole('button', { name: 'Cancel', exact: true })
    .click()
  await expect(tab(page, 'Assigned to me')).toHaveAccessibleName(
    /^Assigned to me 2\b/,
  )
  await page.getByRole('button', { name: 'Approve demo changes' }).click()
  await confirm(page, 'Approve demo changes?')
  await expect(
    page.getByRole('heading', { name: draftName, exact: true }),
  ).toBeFocused()
  await page
    .getByRole('button', { name: 'View details for Ceramic Everyday Cup' })
    .click()
  await page.getByRole('button', { name: 'Request demo changes' }).click()
  await expect(
    page
      .getByRole('dialog', { name: 'Request demo changes?' })
      .getByRole('button', { name: 'Confirm demo action' }),
  ).toBeDisabled()
  await page
    .getByRole('textbox', { name: 'Review note' })
    .fill('Please add care instructions.')
  await confirm(page, 'Request demo changes?')
  await expect(page.getByRole('status')).toContainText('changes requested')
  await expect(tab(page, 'Assigned to me')).toHaveAccessibleName(
    /^Assigned to me 0\b/,
  )
  await expect(
    page.getByRole('heading', { name: 'No reviews assigned to you' }),
  ).toBeVisible()
  await tab(page, 'Completed').click()
  await expect(page.getByRole('tabpanel')).toContainText('Changes requested')
  await page
    .getByRole('button', { name: 'View details for Ceramic Everyday Cup' })
    .click()
  await expect(
    page.getByRole('complementary', { name: 'Page context' }),
  ).toContainText('Please add care instructions.')
  await expect(
    page.getByRole('button', { name: 'Approve demo changes' }),
  ).toHaveCount(0)
  await openDemoReset(page)
  await page
    .getByRole('dialog', {
      name: 'Reset catalog and Home demo for Atelier Kyoto?',
    })
    .getByRole('button', { name: 'Confirm demo reset' })
    .click()
  await expect(tab(page, 'Drafts')).toHaveAccessibleName(/^Drafts 2\b/)
  await expect(tab(page, 'Completed')).toHaveAccessibleName(/^Completed 1\b/)
})
test('My work persists across routes, namespaces and history; reload restores the demo', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto(path)
  await openDraft(page)
  await page.getByRole('button', { name: 'Discard demo draft' }).click()
  await confirm(page, 'Discard demo draft?')
  const nav = page.getByRole('navigation', {
    name: 'Main navigation',
    exact: true,
  })
  await nav.getByRole('link', { name: 'Overview', exact: true }).click()
  await expect(
    page.getByRole('region', { name: 'Store summary' }),
  ).toContainText('18')
  await expect(
    page.getByRole('button', { name: 'Edit demo draft', exact: true }),
  ).toHaveCount(0)
  await page.goBack()
  await expect(tab(page, 'Drafts')).toHaveAccessibleName(/^Drafts 1\b/)
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Studio Berlin/ }).click()
  await expect(page).toHaveURL(/studio-berlin\/home\/my-work$/)
  await expect(page.getByRole('heading', { name: 'No work yet' })).toBeVisible()
  await expect(tab(page, 'Drafts')).toHaveAccessibleName(/^Drafts 0\b/)
  await page.getByRole('button', { name: /Workspace context/ }).click()
  await page.getByRole('option', { name: /Atelier Kyoto/ }).click()
  await expect(tab(page, 'Drafts')).toHaveAccessibleName(/^Drafts 1\b/)
  await page.reload()
  await expect(
    page.getByRole('heading', { name: 'My work', exact: true }),
  ).toBeVisible()
})
test('Focus and responsive layouts preserve the local editor, remain accessible and do not overflow', async ({
  page,
}, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto(path)
  await editDraft(page)
  await page
    .getByRole('textbox', { name: 'Title', exact: true })
    .fill('Focus basin')
  await page.getByRole('button', { name: 'Focus mode', exact: true }).click()
  await page.getByRole('button', { name: 'Exit Focus Mode' }).click()
  await page.getByRole('button', { name: 'Inspector', exact: true }).click()
  await expect(
    page.getByRole('textbox', { name: 'Title', exact: true }),
  ).toHaveValue('Focus basin')
  await page.screenshot({
    path: testInfo.outputPath('my-work-editing-inspector.png'),
  })
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  for (const width of [900, 430, 320]) {
    await page.setViewportSize({ width, height: 932 })
    await expect(
      page.getByRole('dialog', { name: 'Page context', exact: true }),
    ).toBeVisible()
    await expect(
      page.getByRole('textbox', { name: 'Title', exact: true }),
    ).toHaveValue('Focus basin')
    expect(await page.locator('#work-inspector-heading').count()).toBe(1)
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
  }
  await page.keyboard.press('Escape')
  await page
    .getByRole('dialog', { name: 'Discard unsaved edits?' })
    .getByRole('button', { name: 'Cancel', exact: true })
    .click()
  await expect(
    page.getByRole('textbox', { name: 'Title', exact: true }),
  ).toHaveValue('Focus basin')
  await page.getByRole('button', { name: 'Save demo draft' }).click()
  await page.screenshot({
    path: testInfo.outputPath('my-work-mobile-inspector.png'),
  })
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.getByRole('button', { name: 'Close inspector' }).click()
  await page.screenshot({ path: testInfo.outputPath('my-work-mobile.png') })
  for (const theme of ['Light', 'Dark']) {
    await page.setViewportSize({ width: 1440, height: 1000 })
    await page.getByRole('radio', { name: theme, exact: true }).press('Space')
    await expect(page.locator('html')).toHaveAttribute(
      'data-theme',
      theme.toLowerCase(),
    )
    await page.screenshot({
      path: testInfo.outputPath(`my-work-${theme.toLowerCase()}.png`),
    })
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  }
})
