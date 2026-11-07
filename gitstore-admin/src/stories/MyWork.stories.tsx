import { useState } from 'react'
import { RouterProvider } from '@tanstack/react-router'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within } from 'storybook/test'
import { createAppRouter } from '../app/router'
import type { WorkScenario, WorkTab } from '../features/home/my-work/model'
import type { NamespaceId } from '../data/demo'
import type { Theme, MotionPreference } from '../state/preferences'
function MyWorkPreview({
  namespace = 'atelier-kyoto',
  scenario,
  tab = 'drafts',
  theme = 'light',
  motion = 'system',
}: {
  namespace?: NamespaceId
  scenario?: WorkScenario
  tab?: WorkTab
  theme?: Theme
  motion?: MotionPreference
}) {
  const [router] = useState(() =>
    createAppRouter({
      initialPath: `/w/${namespace}/home/my-work`,
      context: {
        myWorkScenario: scenario,
        myWorkTab: tab,
        preferences: { initialTheme: theme, initialMotion: motion },
      },
    }),
  )
  return <RouterProvider router={router} />
}
const meta = {
  title: 'Pages/Home/My work',
  component: MyWorkPreview,
  parameters: { layout: 'fullscreen' },
  args: {
    namespace: 'atelier-kyoto',
    tab: 'drafts',
    theme: 'light',
    motion: 'system',
  },
  render: (args) => <MyWorkPreview key={JSON.stringify(args)} {...args} />,
} satisfies Meta<typeof MyWorkPreview>
export default meta
type Story = StoryObj<typeof meta>
export const Drafts: Story = {}
export const Submitted: Story = { args: { tab: 'submitted' } }
export const AssignedToMe: Story = { args: { tab: 'assigned' } }
export const Completed: Story = { args: { tab: 'completed' } }
export const Dark: Story = { args: { theme: 'dark' } }
export const EmptyWorkspace: Story = { args: { namespace: 'studio-berlin' } }
export const NoMatches: Story = { args: { scenario: 'filtered-empty' } }
export const Loading: Story = { args: { scenario: 'loading' } }
export const Unavailable: Story = { args: { scenario: 'unavailable' } }
export const ReadOnly: Story = { args: { scenario: 'read-only' } }
export const ReducedMotion: Story = { args: { motion: 'reduced' } }
export const Mobile: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const Tablet: Story = {
  globals: { viewport: { value: 'tablet', isRotated: false } },
}
async function openInspector(canvasElement: HTMLElement) {
  await userEvent.click(
    await within(canvasElement).findByRole(
      'button',
      {
        name: 'View details for Kanso Stone Basin',
      },
      { timeout: 10_000 },
    ),
  )
  const body = within(canvasElement.ownerDocument.body)
  await body.findByRole(
    'button',
    { name: 'Edit demo draft' },
    { timeout: 10_000 },
  )
  return body
}
export const ChangePreview: Story = {
  play: async ({ canvasElement }) => {
    await openInspector(canvasElement)
  },
}
export const Editing: Story = {
  play: async ({ canvasElement }) => {
    const body = await openInspector(canvasElement)
    await userEvent.click(body.getByRole('button', { name: 'Edit demo draft' }))
    await expect(
      await body.findByRole('textbox', { name: 'Title' }, { timeout: 10_000 }),
    ).toBeVisible()
  },
}
export const UnsavedChanges: Story = {
  play: async ({ canvasElement }) => {
    const body = await openInspector(canvasElement)
    await userEvent.click(body.getByRole('button', { name: 'Edit demo draft' }))
    await userEvent.type(
      await body.findByRole('textbox', { name: 'Title' }, { timeout: 10_000 }),
      ' — Large',
    )
    await expect(
      body.getByRole('button', { name: 'Submit demo draft' }),
    ).toBeDisabled()
  },
}
export const UnsavedConfirmation: Story = {
  play: async ({ canvasElement }) => {
    const body = await openInspector(canvasElement)
    await userEvent.click(body.getByRole('button', { name: 'Edit demo draft' }))
    await userEvent.type(
      await body.findByRole('textbox', { name: 'Title' }, { timeout: 10_000 }),
      ' — Large',
    )
    await userEvent.click(body.getByRole('button', { name: 'Close inspector' }))
    await expect(
      await body.findByRole(
        'dialog',
        { name: 'Discard unsaved edits?' },
        { timeout: 10_000 },
      ),
    ).toBeVisible()
  },
}
export const SubmitConfirmation: Story = {
  play: async ({ canvasElement }) => {
    const body = await openInspector(canvasElement)
    await userEvent.click(
      body.getByRole('button', { name: 'Submit demo draft' }),
    )
    await expect(
      await body.findByRole(
        'dialog',
        { name: 'Submit demo draft?' },
        { timeout: 10_000 },
      ),
    ).toBeVisible()
  },
}
export const ReviewConfirmation: Story = {
  args: { tab: 'assigned' },
  play: async ({ canvasElement }) => {
    await userEvent.click(
      await within(canvasElement).findByRole(
        'button',
        {
          name: 'View details for Kanso Stone Basin',
        },
        { timeout: 10_000 },
      ),
    )
    const body = within(canvasElement.ownerDocument.body)
    await userEvent.click(
      await body.findByRole(
        'button',
        { name: 'Request demo changes' },
        { timeout: 10_000 },
      ),
    )
    await expect(
      await body.findByRole(
        'textbox',
        { name: 'Review note' },
        { timeout: 10_000 },
      ),
    ).toBeVisible()
  },
}

export const TabletSubmitConfirmation: Story = {
  globals: { viewport: { value: 'tablet', isRotated: false } },
  play: SubmitConfirmation.play,
}
export const MobileSubmitConfirmation: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
  play: SubmitConfirmation.play,
}
