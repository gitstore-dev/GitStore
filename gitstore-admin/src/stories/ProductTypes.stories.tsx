import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within } from 'storybook/test'
import { StructurePreview } from './StructurePreview'
const meta = {
  title: 'Pages/Catalog/Product types',
  component: StructurePreview,
  parameters: { layout: 'fullscreen' },
  args: { domain: 'product-types', theme: 'light' },
} satisfies Meta<typeof StructurePreview>
export default meta
type Story = StoryObj<typeof meta>
export const Rows: Story = {}
export const Cards: Story = { args: { query: 'view=grid' } }
export const Detail: Story = { args: { detail: 'apparel' } }
export const Inspector: Story = { args: { detail: 'apparel', inspector: true } }
export const EmptyStore: Story = { args: { namespace: 'studio-berlin' } }
export const NoResults: Story = { args: { query: 'q=missing' } }
export const Loading: Story = { args: { scenario: 'loading' } }
export const Unavailable: Story = { args: { scenario: 'unavailable' } }
export const MissingRelationships: Story = {
  args: { scenario: 'missing-relationships', detail: 'apparel' },
}
export const LongValues: Story = {
  args: { scenario: 'long-values', query: 'view=grid' },
}
export const MoreResults: Story = { args: { scenario: 'many' } }
export const Dark: Story = { args: { theme: 'dark' } }
export const Mobile: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const ReducedMotion: Story = { args: { motion: 'reduced' } }
export const RelationshipNavigation: Story = {
  args: { detail: 'apparel' },
  play: async ({ canvasElement }) => {
    const screen = within(canvasElement)
    await userEvent.click(await screen.findByRole('link', { name: 'Color' }), {
      skipHover: true,
    })
    await expect(
      await screen.findByRole('heading', { name: 'Example choices' }),
    ).toBeVisible()
    await userEvent.click(screen.getByRole('link', { name: 'Apparel' }), {
      skipHover: true,
    })
    await expect(
      await screen.findByRole('heading', { name: 'Example Products' }),
    ).toBeVisible()
  },
}
