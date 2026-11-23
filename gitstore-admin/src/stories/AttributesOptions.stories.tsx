import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within } from 'storybook/test'
import { StructurePreview } from './StructurePreview'
const meta = {
  title: 'Pages/Catalog/Attributes & options',
  component: StructurePreview,
  parameters: { layout: 'fullscreen' },
  args: { domain: 'attributes-options', theme: 'light' },
} satisfies Meta<typeof StructurePreview>
export default meta
type Story = StoryObj<typeof meta>
export const ProductOptions: Story = {}
export const Definitions: Story = { args: { query: 'tab=definitions' } }
export const Cards: Story = { args: { query: 'view=grid' } }
export const OptionDetail: Story = {
  args: { detail: 'products/silk-tunic/options/color' },
}
export const DefinitionDetail: Story = { args: { detail: 'definitions/color' } }
export const Inspector: Story = {
  args: { detail: 'definitions/color', inspector: true },
}
export const EmptyStore: Story = { args: { namespace: 'studio-berlin' } }
export const NoResults: Story = { args: { query: 'q=missing' } }
export const Loading: Story = { args: { scenario: 'loading' } }
export const Unavailable: Story = { args: { scenario: 'unavailable' } }
export const UnmatchedSelection: Story = {
  args: {
    detail: 'products/silk-tunic/options/size',
    scenario: 'unmatched-selection',
  },
}
export const MissingOption: Story = {
  args: { detail: 'products/silk-tunic/options/missing' },
}
export const LongValues: Story = {
  args: { scenario: 'long-values', query: 'view=grid' },
}
export const MoreDefinitions: Story = {
  args: { scenario: 'many', query: 'tab=definitions' },
}
export const Dark: Story = { args: { theme: 'dark' } }
export const Mobile: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const ReducedMotion: Story = { args: { motion: 'reduced' } }
export const DefinitionNavigation: Story = {
  play: async ({ canvasElement }) => {
    const screen = within(canvasElement)
    await userEvent.click(
      await screen.findByRole('tab', { name: /Reusable definitions/ }),
    )
    await userEvent.click(
      await screen.findByRole('link', {
        name: /Color Reusable choice example/,
      }),
    )
    await expect(
      await screen.findByRole('heading', { name: 'Example choices' }),
    ).toBeVisible()
    await expect(
      screen.getByRole('complementary', { name: 'Design preview' }),
    ).toBeVisible()
  },
}
