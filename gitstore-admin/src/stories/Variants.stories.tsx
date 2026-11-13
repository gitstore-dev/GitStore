import type { Meta, StoryObj } from '@storybook/react-vite'
import { CatalogBrowserPreview } from './CatalogBrowserPreview'
const meta = {
  title: 'Pages/Catalog/Variants',
  component: CatalogBrowserPreview,
  parameters: { layout: 'fullscreen' },
  args: { domain: 'variants', theme: 'light' },
  render: (args) => (
    <CatalogBrowserPreview key={JSON.stringify(args)} {...args} />
  ),
} satisfies Meta<typeof CatalogBrowserPreview>
export default meta
type Story = StoryObj<typeof meta>
export const Populated: Story = {}
export const Detail: Story = { args: { itemId: 'tunic-natural-m' } }
export const EmptyStore: Story = { args: { namespace: 'studio-berlin' } }
export const NoResults: Story = { args: { query: 'q=unmatched' } }
export const Loading: Story = { args: { scenario: 'loading' } }
export const Unavailable: Story = { args: { scenario: 'unavailable' } }
export const EarlierSnapshot: Story = { args: { scenario: 'stale' } }
export const Dark: Story = { args: { theme: 'dark' } }
export const Mobile: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const ReducedMotion: Story = { args: { motion: 'reduced' } }

export const MoreResults: Story = {
  args: { scenario: 'many', query: 'view=table' },
}
