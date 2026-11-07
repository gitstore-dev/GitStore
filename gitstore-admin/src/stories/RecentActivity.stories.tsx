import { useState } from 'react'
import type { ReactNode } from 'react'
import { RouterProvider, useRouterState } from '@tanstack/react-router'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within, waitFor } from 'storybook/test'
import { navigateHome, settle } from './helpers/shellInteractions'
import { createAppRouter } from '../app/router'
import type { ActivityScenario } from '../features/home/demo/model'
import type { NamespaceId } from '../data/demo'
import type { Theme, MotionPreference } from '../state/preferences'
function StoryRouteBoundary({ children }: { children: ReactNode }) {
  const status = useRouterState({ select: (state) => state.status })
  return (
    <div data-story-route-state={status} style={{ display: 'contents' }}>
      {children}
    </div>
  )
}
function RecentActivityPreview({
  namespace = 'atelier-kyoto',
  scenario,
  theme = 'light',
  motion = 'system',
  startPage = 'recent-activity',
}: {
  namespace?: NamespaceId
  scenario?: ActivityScenario
  theme?: Theme
  motion?: MotionPreference
  startPage?: 'recent-activity' | 'my-work'
}) {
  const [router] = useState(() =>
    createAppRouter({
      initialPath: `/w/${namespace}/home/${startPage}`,
      context: {
        activityScenario: scenario,
        preferences: { initialTheme: theme, initialMotion: motion },
      },
    }),
  )
  return <RouterProvider router={router} InnerWrap={StoryRouteBoundary} />
}
const meta = {
  title: 'Pages/Home/Recent activity',
  component: RecentActivityPreview,
  parameters: { layout: 'fullscreen' },
  args: { namespace: 'atelier-kyoto', theme: 'light', motion: 'system' },
  render: (args) => (
    <RecentActivityPreview key={JSON.stringify(args)} {...args} />
  ),
} satisfies Meta<typeof RecentActivityPreview>
export default meta
type Story = StoryObj<typeof meta>
export const Populated: Story = {}
export const Dark: Story = { args: { theme: 'dark' } }
export const EmptyStore: Story = { args: { namespace: 'studio-berlin' } }
export const Loading: Story = { args: { scenario: 'loading' } }
export const Unavailable: Story = { args: { scenario: 'unavailable' } }
export const Stale: Story = { args: { scenario: 'stale' } }
export const Restricted: Story = { args: { scenario: 'restricted' } }
export const ReducedMotion: Story = { args: { motion: 'reduced' } }
export const Tablet: Story = {
  globals: { viewport: { value: 'tablet', isRotated: false } },
}
export const Mobile: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const NoResults: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.type(
      await within(canvasElement).findByRole(
        'searchbox',
        {
          name: 'Search activity',
        },
        { timeout: 10_000 },
      ),
      'No matching title',
    )
    await expect(
      await within(canvasElement).findByRole(
        'heading',
        {
          name: 'No matching activity',
        },
        { timeout: 10_000 },
      ),
    ).toBeVisible()
  },
}
export const OlderResults: Story = {
  play: async ({ canvasElement }) => {
    await clickSettled(
      await within(canvasElement).findByRole(
        'button',
        { name: 'Load more' },
        { timeout: 10_000 },
      ),
    )
    await expect(
      within(canvasElement).getByText(
        '20 of 24 matching events · 24 total demo events',
      ),
    ).toBeVisible()
  },
}
async function nextPaint() {
  await new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  )
}
async function clickSettled(element: HTMLElement) {
  await nextPaint()
  await waitFor(
    () => {
      const route = element.ownerDocument.querySelector<HTMLElement>(
        '[data-story-route-state]',
      )
      if (route?.dataset.storyRouteState !== 'idle')
        throw new Error('Waiting for the route to settle')
    },
    { timeout: 10_000 },
  )
  await nextPaint()
  const animations = new Set<Animation>()
  for (let node: HTMLElement | null = element; node; node = node.parentElement)
    node.getAnimations().forEach((animation) => animations.add(animation))
  await Promise.all(
    [...animations].map((animation) =>
      animation.finished.catch(() => undefined),
    ),
  )
  if (element.tagName === 'BUTTON') {
    element.focus()
    await userEvent.keyboard('{Enter}')
  } else await userEvent.click(element)
  await new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  )
}
async function oldVersion(canvasElement: HTMLElement) {
  const canvas = within(canvasElement)
  const body = within(canvasElement.ownerDocument.body)
  await userEvent.type(
    await canvas.findByRole(
      'searchbox',
      { name: 'Search activity' },
      { timeout: 10_000 },
    ),
    'Kanso',
  )
  const filtersButton = canvas.queryByRole('button', { name: /^Filters/ })
  if (filtersButton) await clickSettled(filtersButton)
  await clickSettled(body.getByRole('button', { name: /Recorded date/ }))
  await clickSettled(
    await body.findByRole(
      'option',
      { name: '31 Oct 2026' },
      { timeout: 10_000 },
    ),
  )
  if (filtersButton)
    await clickSettled(
      await body.findByRole(
        'button',
        { name: /^Show \d+ events?$/ },
        { timeout: 10_000 },
      ),
    )
  await clickSettled(
    await canvas.findByRole(
      'button',
      {
        name: 'View details for Kanso Stone Basin — Original',
      },
      { timeout: 10_000 },
    ),
  )
  await body.findByRole(
    'button',
    { name: 'Restore this version in demo' },
    { timeout: 10_000 },
  )
  const panel = canvasElement.ownerDocument.querySelector<HTMLElement>(
    '[aria-label="Page context"]',
  )
  if (panel) {
    await nextPaint()
    await Promise.all(
      panel
        .getAnimations({ subtree: true })
        .map((animation) => animation.finished.catch(() => undefined)),
    )
  }
  return body
}
export const HistoricalPreview: Story = {
  play: async ({ canvasElement }) => {
    await oldVersion(canvasElement)
  },
}
export const CurrentVersion: Story = {
  play: async ({ canvasElement }) => {
    await clickSettled(
      (
        await within(canvasElement).findAllByRole(
          'button',
          {
            name: /^View details for/,
          },
          { timeout: 10_000 },
        )
      )[0]!,
    )
    await expect(
      await within(canvasElement.ownerDocument.body).findByRole(
        'button',
        {
          name: 'Restore this version in demo',
        },
        { timeout: 10_000 },
      ),
    ).toBeDisabled()
  },
}
export const RecoveryConfirmation: Story = {
  play: async ({ canvasElement }) => {
    const body = await oldVersion(canvasElement)
    await clickSettled(
      body.getByRole('button', { name: 'Restore this version in demo' }),
    )
    await expect(
      await body.findByRole(
        'dialog',
        {
          name: 'Restore this version in demo?',
        },
        { timeout: 10_000 },
      ),
    ).toBeVisible()
  },
}
export const RecoveryError: Story = {
  args: { scenario: 'recovery-error' },
  play: async ({ canvasElement }) => {
    const body = await oldVersion(canvasElement)
    await expect(
      body.getByRole('button', { name: 'Restore this version in demo' }),
    ).toBeDisabled()
    await expect(
      body.getByText(
        'A related item is missing or has the wrong type. This version cannot be restored.',
      ),
    ).toBeVisible()
  },
}
export const CompletedRecovery: Story = {
  play: async ({ canvasElement }) => {
    const body = await oldVersion(canvasElement)
    await clickSettled(
      body.getByRole('button', { name: 'Restore this version in demo' }),
    )
    await clickSettled(
      await body.findByRole(
        'button',
        { name: 'Confirm demo recovery' },
        { timeout: 10_000 },
      ),
    )
    await clickSettled(
      await body.findByRole(
        'button',
        { name: 'Close inspector' },
        { timeout: 10_000 },
      ),
    )
    await navigateHome(canvasElement, 'My work')
    await clickSettled(
      await within(canvasElement).findByRole(
        'tab',
        { name: /^Completed/ },
        { timeout: 10_000 },
      ),
    )
    await clickSettled(
      await within(canvasElement).findByRole(
        'button',
        {
          name: 'View details for Kanso Stone Basin — Original',
        },
        { timeout: 10_000 },
      ),
    )
    const preview = await body.findByRole(
      'region',
      { name: 'Change preview' },
      { timeout: 10_000 },
    )
    await settle(preview)
    await expect(preview).toBeVisible()
  },
}
export const MobileRecovery: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
  play: RecoveryConfirmation.play,
}
export const PreservedDraftBuffer: Story = {
  args: { startPage: 'my-work' },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const body = within(canvasElement.ownerDocument.body)
    await clickSettled(
      await canvas.findByRole(
        'button',
        {
          name: 'View details for Kanso Stone Basin',
        },
        { timeout: 10_000 },
      ),
    )
    await clickSettled(
      await body.findByRole(
        'button',
        { name: 'Edit demo draft' },
        { timeout: 10_000 },
      ),
    )
    const title = await body.findByRole(
      'textbox',
      { name: 'Title' },
      { timeout: 10_000 },
    )
    await userEvent.clear(title)
    await userEvent.type(title, 'Kanso Stone Basin — Unsaved')
    await navigateHome(canvasElement, 'Recent activity')
    await canvas.findByRole(
      'searchbox',
      { name: 'Search activity' },
      { timeout: 10_000 },
    )
    await navigateHome(canvasElement, 'My work')
    await clickSettled(
      await canvas.findByRole(
        'button',
        {
          name: 'View details for Kanso Stone Basin',
        },
        { timeout: 10_000 },
      ),
    )
    await expect(
      await body.findByRole('textbox', { name: 'Title' }, { timeout: 10_000 }),
    ).toHaveValue('Kanso Stone Basin — Unsaved')
  },
}
