import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, within } from 'storybook/test'
import { TechnicalDetails } from '../components/TechnicalDetails'
import { PreferencesProvider } from '../state/preferences'
import type { Theme } from '../state/preferences'
function TechnicalDetailsPreview({
  resourceType = 'Product',
  sourcePath = 'catalog/products/kanso-stone-basin.md',
  theme = 'light',
  defaultExpanded = false,
}: {
  resourceType?: string
  sourcePath?: string
  theme?: Theme
  defaultExpanded?: boolean
}) {
  return (
    <PreferencesProvider initialTheme={theme}>
      <div
        style={{
          background: 'var(--surface)',
          color: 'var(--ink)',
          padding: 24,
          maxWidth: 300,
          minHeight: 260,
        }}
      >
        <TechnicalDetails
          resourceType={resourceType}
          sourcePath={sourcePath}
          defaultExpanded={defaultExpanded}
        />
      </div>
    </PreferencesProvider>
  )
}
const meta = {
  title: 'Foundation/Technical details',
  component: TechnicalDetailsPreview,
  args: {
    theme: 'light',
    resourceType: 'Product',
    sourcePath: 'catalog/products/kanso-stone-basin.md',
  },
} satisfies Meta<typeof TechnicalDetailsPreview>
export default meta
type Story = StoryObj<typeof meta>
export const Collapsed: Story = {}
export const Expanded: Story = {
  args: { defaultExpanded: true },
  play: async ({ canvasElement }) => {
    const disclosure = canvasElement.querySelector('details')!
    await expect(disclosure).toHaveAttribute('open')
    await expect(
      within(canvasElement).getByText('Resource type', { exact: true }),
    ).toBeVisible()
    await expect(canvasElement.querySelector('code')!).toBeVisible()
  },
}
export const Dark: Story = {
  args: { theme: 'dark', defaultExpanded: true },
  play: Expanded.play,
}
export const MediaFile: Story = {
  args: {
    defaultExpanded: true,
    resourceType: 'File',
    sourcePath: 'media/basin-detail.md',
  },
  play: Expanded.play,
}
export const LongPath: Story = {
  args: {
    defaultExpanded: true,
    sourcePath: `catalog/products/${'handcrafted-'.repeat(12)}stone-basin.md`,
  },
  play: Expanded.play,
}
