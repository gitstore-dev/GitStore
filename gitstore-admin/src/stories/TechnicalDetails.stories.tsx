import type { Meta, StoryObj } from '@storybook/react-vite'
import { userEvent, within } from 'storybook/test'
import { TechnicalDetails } from '../components/TechnicalDetails'
import { PreferencesProvider } from '../state/preferences'
import type { Theme } from '../state/preferences'
function TechnicalDetailsPreview({
  resourceType = 'Product',
  sourcePath = 'catalog/products/kanso-stone-basin.md',
  theme = 'light',
}: {
  resourceType?: string
  sourcePath?: string
  theme?: Theme
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
        <TechnicalDetails resourceType={resourceType} sourcePath={sourcePath} />
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
  play: async ({ canvasElement }) => {
    const summary = within(canvasElement).getByText('Technical details', {
      exact: true,
    })
    summary.focus()
    await userEvent.keyboard('{Enter}')
  },
}
export const Dark: Story = { args: { theme: 'dark' }, play: Expanded.play }
export const MediaFile: Story = {
  args: { resourceType: 'File', sourcePath: 'media/basin-detail.md' },
  play: Expanded.play,
}
export const LongPath: Story = {
  args: {
    sourcePath: `catalog/products/${'handcrafted-'.repeat(12)}stone-basin.md`,
  },
  play: Expanded.play,
}
