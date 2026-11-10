import { useState } from 'react'
import { RouterProvider } from '@tanstack/react-router'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within, waitFor } from 'storybook/test'
import { createAppRouter } from '../app/router'
import type { NamespaceId } from '../data/demo'
import type { ProductScenario } from '../features/demo/model'
import type { Theme, MotionPreference } from '../state/preferences'
import type { LayoutMode } from '../shell/WorkspaceLayout'
function ProductsPreview({
  namespace = 'atelier-kyoto',
  path = '',
  scenario,
  theme = 'light',
  motion = 'system',
  layout = 'standard',
}: {
  namespace?: NamespaceId
  path?: string
  scenario?: ProductScenario
  theme?: Theme
  motion?: MotionPreference
  layout?: LayoutMode
}) {
  const [router] = useState(() =>
    createAppRouter({
      initialPath: `/w/${namespace}/catalog/products${path}`,
      context: {
        productScenario: scenario,
        initialLayout: layout,
        preferences: { initialTheme: theme, initialMotion: motion },
      },
    }),
  )
  return <RouterProvider router={router} />
}
const meta = {
  title: 'Pages/Catalog/Products',
  component: ProductsPreview,
  parameters: { layout: 'fullscreen' },
  args: { namespace: 'atelier-kyoto', theme: 'light' },
  render: (args) => <ProductsPreview key={JSON.stringify(args)} {...args} />,
} satisfies Meta<typeof ProductsPreview>
export default meta
type Story = StoryObj<typeof meta>
export const ListView: Story = {}
export const GridView: Story = { args: { path: '?view=grid' } }
export const Dark: Story = { args: { theme: 'dark', path: '?view=grid' } }
export const Empty: Story = { args: { namespace: 'studio-berlin' } }
export const NoResults: Story = { args: { path: '?q=No%20such%20product' } }
export const ProductView: Story = { args: { path: '/stone-basin' } }
export const Create: Story = { args: { path: '/new' } }
export const Edit: Story = { args: { path: '/stone-basin/edit' } }
export const FocusEditor: Story = {
  args: { path: '/stone-basin/edit', layout: 'focus' },
}
export const UnsupportedMarkdown: Story = {
  args: { path: '/stone-basin/edit', scenario: 'unsupported' },
}
export const StaleEditor: Story = {
  args: { path: '/stone-basin/edit', scenario: 'stale-edit' },
}
export const BlockedDeletion: Story = { args: { path: '/silk-tunic' } }
export const ReadOnlyVariants: Story = { args: { path: '/silk-tunic' } }
export const Deleting: Story = {
  args: { path: '/stone-basin', scenario: 'deleting' },
}
export const Deleted: Story = {
  args: { path: '/stone-basin', scenario: 'deleted' },
}
export const Tablet: Story = {
  args: { path: '/stone-basin/edit' },
  globals: { viewport: { value: 'tablet', isRotated: false } },
}
export const Mobile: Story = {
  args: { path: '/stone-basin/edit' },
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const ReducedMotion: Story = {
  args: { motion: 'reduced', path: '?view=grid' },
}
export const Validation: Story = {
  args: { path: '/new' },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(
      await canvas.findByRole(
        'button',
        { name: 'Create demo product' },
        { timeout: 10000 },
      ),
    )
    await expect(canvas.getByRole('alert')).toHaveTextContent('Enter a Title')
  },
}
export const DeleteConfirmation: Story = {
  args: { path: '/stone-basin' },
  play: async ({ canvasElement }) => {
    await userEvent.click(
      await within(canvasElement).findByRole(
        'button',
        { name: 'Delete demo product' },
        { timeout: 10000 },
      ),
    )
    await expect(
      await within(canvasElement.ownerDocument.body).findByRole('dialog', {
        name: 'Start demo deletion?',
      }),
    ).toBeVisible()
  },
}
export const DiscardConfirmation: Story = {
  args: { path: '/stone-basin/edit' },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const input = await canvas.findByRole(
      'textbox',
      { name: /^Title$/ },
      { timeout: 10000 },
    )
    await userEvent.clear(input)
    await userEvent.type(input, 'Unsaved product Title')
    await userEvent.click(canvas.getByRole('button', { name: /^Cancel$/ }))
    await expect(
      await within(canvasElement.ownerDocument.body).findByRole('dialog', {
        name: 'Discard unsaved Product edits?',
      }),
    ).toBeVisible()
  },
}

async function expandAndReturn({
  canvasElement,
}: {
  canvasElement: HTMLElement
}) {
  const canvas = within(canvasElement)
  const item = await canvas.findByRole('link', {
    name: /^View Kanso Stone Basin(?: |$)/,
  })
  await userEvent.click(item)
  await expect(
    await canvas.findByRole('heading', { name: 'Kanso Stone Basin', level: 1 }),
  ).toBeVisible()
  await waitFor(() =>
    expect(canvasElement.ownerDocument.documentElement).not.toHaveAttribute(
      'data-product-transition',
    ),
  )
  await userEvent.click(
    within(canvas.getByRole('main')).getByRole('link', {
      name: 'Products',
    }),
  )
  await waitFor(() =>
    expect(
      canvas.getByRole('link', { name: /^View Kanso Stone Basin(?: |$)/ }),
    ).toHaveFocus(),
  )
}
export const TableExpansion: Story = { play: expandAndReturn }
export const GridExpansionDark: Story = {
  args: { path: '?view=grid', theme: 'dark' },
  play: expandAndReturn,
}
export const MobileExpansion: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
  play: expandAndReturn,
}
export const ReducedMotionExpansion: Story = {
  args: { motion: 'reduced' },
  play: expandAndReturn,
}
