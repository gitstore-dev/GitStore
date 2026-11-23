import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within } from 'storybook/test'
import { MediaPreview } from './MediaPreview'
const meta = {
  title: 'Pages/Catalog/Media',
  component: MediaPreview,
  parameters: { layout: 'fullscreen' },
  args: { theme: 'light' },
} satisfies Meta<typeof MediaPreview>
export default meta
type Story = StoryObj<typeof meta>
export const Cards: Story = {}
export const Rows: Story = { args: { query: 'view=table' } }
export const Dark: Story = { args: { theme: 'dark' } }
export const Mobile: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const ReducedMotion: Story = { args: { motion: 'reduced' } }
export const EmptyLibrary: Story = { args: { namespace: 'studio-berlin' } }
export const NoResults: Story = { args: { query: 'q=missing-photo' } }
export const Detail: Story = { args: { itemId: 'lookbook' } }
export const Loading: Story = { args: { scenario: 'loading' } }
export const Unavailable: Story = { args: { scenario: 'unavailable' } }
export const Stale: Story = { args: { scenario: 'stale' } }
export const ReadOnly: Story = {
  args: { scenario: 'read-only', itemId: 'basin-image' },
}
export const MissingPreview: Story = {
  args: { scenario: 'missing-preview', itemId: 'basin-image' },
}
export const FailedPreview: Story = {
  args: { scenario: 'failed-preview', itemId: 'basin-image' },
}
export const NonImageFile: Story = {
  args: { scenario: 'document', itemId: 'basin-image' },
}
export const Ready: Story = { args: { scenario: 'ready' } }
export const Unused: Story = {
  args: { scenario: 'unused', itemId: 'basin-image' },
}
export const LongFileName: Story = { args: { scenario: 'long-name' } }
export const MoreResults: Story = {
  args: { scenario: 'many', query: 'view=table' },
}
export const PreviewDialog: Story = {
  args: { itemId: 'lookbook' },
  play: async ({ canvasElement }) => {
    const screen = within(canvasElement)
    const trigger = await screen.findByRole('button', {
      name: 'Enlarge preview',
    })
    await userEvent.click(trigger)
    await expect(
      await within(canvasElement.ownerDocument.body).findByRole('dialog', {
        name: 'Preview lookbook-hero.webp',
      }),
    ).toBeVisible()
  },
}
export const PreviewKeyboard: Story = {
  args: { itemId: 'lookbook' },
  play: async ({ canvasElement }) => {
    const screen = within(canvasElement),
      trigger = await screen.findByRole('button', { name: 'Enlarge preview' })
    trigger.focus()
    await userEvent.keyboard('{Enter}')
    const dialog = await within(canvasElement.ownerDocument.body).findByRole(
      'dialog',
      { name: 'Preview lookbook-hero.webp' },
    )
    await userEvent.click(within(dialog).getByRole('button', { name: '100%' }))
    await expect(
      within(dialog).getByRole('button', { name: '100%' }),
    ).toHaveAttribute('aria-pressed', 'true')
    await userEvent.keyboard('{Escape}')
    await expect(trigger).toHaveFocus()
  },
}
