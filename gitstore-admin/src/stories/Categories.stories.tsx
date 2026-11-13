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
export const Spatial: Story = { args: { query: 'view=spatial' } }
export const MissingParent: Story = {
  args: { scenario: 'missing-parent', itemId: 'ceramics' },
}
export const Cycle: Story = { args: { scenario: 'cycle' } }
export const MapInspector: Story = {
  args: { query: 'view=spatial&selected=ceramics' },
}

export const MoreResults: Story = {
  args: { scenario: 'many', query: 'view=table' },
}

export const SpatialKeyboard: Story = {
  args: { query: 'view=spatial' },
  play: async ({ canvasElement }) => {
    const screen = within(canvasElement)
    const map = await screen.findByRole('region', {
      name: 'Pan and zoom categories',
    })
    map.focus()
    await userEvent.keyboard('{ArrowRight}+')
    await expect(screen.getByText('115%', { exact: true })).toBeVisible()
    await userEvent.keyboard('0')
    await userEvent.click(
      screen.getByRole('button', { name: /^Ceramics View category guidance/ }),
    )
    const inspect = screen.queryByRole('button', {
      name: 'Inspect selected category',
    })
    if (inspect) await userEvent.click(inspect)
    await expect(
      await within(canvasElement.ownerDocument.body).findByRole('heading', {
        name: /^Ceramics$/,
      }),
    ).toBeVisible()
  },
}
