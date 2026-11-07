import { expect, userEvent, waitFor, within } from 'storybook/test'

export async function settle(element: HTMLElement) {
  await new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  )
  const animations = new Set<Animation>()
  for (let node: HTMLElement | null = element; node; node = node.parentElement)
    node.getAnimations().forEach((animation) => animations.add(animation))
  await Promise.all(
    [...animations].map((animation) =>
      animation.finished.catch(() => undefined),
    ),
  )
}
export async function openNavigation(canvasElement: HTMLElement) {
  const canvas = within(canvasElement)
  const body = within(canvasElement.ownerDocument.body)
  await userEvent.click(
    await canvas.findByRole(
      'button',
      { name: 'Open navigation menu' },
      { timeout: 10_000 },
    ),
  )
  const drawer = await body.findByRole(
    'dialog',
    { name: 'Mobile navigation' },
    { timeout: 10_000 },
  )
  await settle(drawer)
  await expect(drawer).toBeVisible()
  return drawer
}
/** Use the navigation actually available at the canvas width, including rails. */
export async function navigateHome(
  canvasElement: HTMLElement,
  title: 'My work' | 'Recent activity',
) {
  const canvas = within(canvasElement)
  const body = within(canvasElement.ownerDocument.body)
  const link = canvas.queryByRole('link', { name: title })
  const viaCommand = !!body.queryByRole('dialog', { name: 'Page context' })
  if (viaCommand) {
    // The inspector is modal on small screens. Use the supported global command
    // shortcut to navigate without discarding its unsaved draft buffer.
    await userEvent.keyboard('{Control>}k{/Control}')
    const search = await body.findByRole(
      'searchbox',
      { name: 'Search pages' },
      { timeout: 10_000 },
    )
    await userEvent.type(search, title)
    await userEvent.click(
      await body.findByRole(
        'option',
        { name: new RegExp(title) },
        { timeout: 10_000 },
      ),
    )
  } else if (link) await userEvent.click(link)
  else if (canvas.queryByRole('button', { name: 'Open navigation menu' })) {
    const drawer = await openNavigation(canvasElement)
    await userEvent.click(within(drawer).getByRole('link', { name: title }))
  } else {
    await userEvent.click(
      await canvas.findByRole(
        'button',
        { name: 'Home pages' },
        { timeout: 10_000 },
      ),
    )
    const menuItem = await body.findByRole(
      'menuitem',
      { name: title },
      { timeout: 10_000 },
    )
    await settle(menuItem)
    await userEvent.click(menuItem)
  }
  await canvas.findByRole(
    'heading',
    { name: title, level: 1 },
    { timeout: 10_000 },
  )
  await waitFor(
    () => {
      const route = canvasElement.querySelector<HTMLElement>(
        '[data-story-route-state]',
      )
      if (route && route.dataset.storyRouteState !== 'idle')
        throw new Error('Waiting for navigation')
    },
    { timeout: 10_000 },
  )
  await settle(canvasElement)
  // Layout mode survives route changes. Close the new page's default inspector
  // before trying to activate background controls; its new page owns no dirty edit.
  if (viaCommand && body.queryByRole('dialog', { name: 'Page context' })) {
    await userEvent.click(body.getByRole('button', { name: 'Close inspector' }))
    await waitFor(
      () => {
        if (body.queryByRole('dialog', { name: 'Page context' }))
          throw new Error('Waiting for inspector dismissal')
      },
      { timeout: 10_000 },
    )
    await settle(canvasElement)
  }
}
export async function openProfile(canvasElement: HTMLElement) {
  const canvas = within(canvasElement)
  const body = within(canvasElement.ownerDocument.body)
  await canvas.findByRole(
    'button',
    { name: /Workspace context/ },
    { timeout: 10_000 },
  )
  await settle(canvasElement)
  if (canvas.queryByRole('button', { name: 'Open navigation menu' })) {
    const drawer = await openNavigation(canvasElement)
    await userEvent.click(
      within(drawer).getByRole('button', { name: /Elena Rostova/ }),
    )
  } else
    await userEvent.click(
      await canvas.findByRole(
        'button',
        { name: /Elena Rostova/ },
        { timeout: 10_000 },
      ),
    )
  const menu = await body.findByRole('menu', {}, { timeout: 10_000 })
  await settle(menu)
  await expect(
    within(menu).getByRole('menuitem', { name: 'Profile & preferences' }),
  ).toBeVisible()
}
