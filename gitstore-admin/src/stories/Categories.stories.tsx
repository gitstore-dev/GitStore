import { expect, userEvent, within } from 'storybook/test'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { CatalogBrowserPreview } from './CatalogBrowserPreview'
const meta = {
  title: 'Pages/Catalog/Categories',
  component: CatalogBrowserPreview,
  parameters: { layout: 'fullscreen' },
  args: { domain: 'categories', theme: 'light' },
  render: (args) => (
    <CatalogBrowserPreview key={JSON.stringify(args)} {...args} />
  ),
} satisfies Meta<typeof CatalogBrowserPreview>
export default meta
type Story = StoryObj<typeof meta>
export const Populated: Story = {}
export const Detail: Story = { args: { itemId: 'ceramics' } }
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
export const Columns: Story = {
  args: { query: 'view=columns&selected=tableware' },
}
export const Tree: Story = { args: { query: 'view=tree' } }
export const Cards: Story = { args: { query: 'view=cards' } }
export const DeepColumns: Story = {
  args: { scenario: 'deep', query: 'view=columns&selected=category-level-12' },
}
export const MissingMedia: Story = {
  args: { scenario: 'missing-media', query: 'view=cards&q=Ceramics' },
}
export const FilteredTree: Story = { args: { query: 'view=tree&q=Tableware' } }
export const MissingParent: Story = {
  args: { scenario: 'missing-parent', itemId: 'ceramics' },
}
export const Cycle: Story = { args: { scenario: 'cycle', query: 'view=tree' } }
export const SelectedColumns: Story = {
  args: { query: 'view=columns&selected=ceramics' },
}

export const MoreResults: Story = {
  args: { scenario: 'many', query: 'view=cards' },
}

export const ColumnsInteraction: Story = {
  args: { query: 'view=columns' },
  play: async ({ canvasElement }) => {
    const screen = within(canvasElement)
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Explore Homeware',
      }),
    )
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Explore Ceramics',
      }),
    )
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Explore Tableware',
      }),
    )
    await expect(
      await screen.findByRole('region', { name: 'Selected category' }),
    ).toBeVisible()
    await expect(
      screen.getByRole('link', { name: /View category/ }),
    ).toBeVisible()
  },
}
