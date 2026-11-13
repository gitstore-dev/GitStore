import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, waitFor, within } from 'storybook/test'
import { CardMediaBackdrop } from '../features/catalog/products/CardMediaBackdrop'
import { ProductMedia } from '../features/catalog/products/ProductMedia'
import { PreferencesProvider } from '../state/preferences'
function Preview({
  image = true,
  theme = 'light',
  reduced = false,
  title = 'Kanso Stone Basin',
}: {
  image?: boolean
  theme?: 'light' | 'dark'
  reduced?: boolean
  title?: string
}) {
  return (
    <PreferencesProvider
      initialTheme={theme}
      initialMotion={reduced ? 'reduced' : 'system'}
    >
      <div style={{ padding: 32, background: 'var(--canvas)', minHeight: 440 }}>
        <a
          href="#preview"
          onClick={(event) => event.preventDefault()}
          data-catalog-card=""
          style={{
            display: 'block',
            width: 280,
            maxWidth: '100%',
            padding: 16,
            border: '1px solid var(--line)',
            borderRadius: 4,
            background: 'var(--surface)',
            color: 'var(--ink)',
            textDecoration: 'none',
          }}
        >
          <CardMediaBackdrop id={image ? 'basin-image' : undefined} />
          <span
            data-product-image=""
            aria-hidden="true"
            style={{ display: 'block' }}
          >
            <ProductMedia id={image ? 'basin-image' : undefined} />
          </span>
          <h2 style={{ font: '500 24px var(--display)' }}>{title}</h2>
          <p>Ready</p>
        </a>
      </div>
    </PreferencesProvider>
  )
}
const meta = {
  title: 'Foundation/Catalog card media',
  component: Preview,
} satisfies Meta<typeof Preview>
export default meta
type Story = StoryObj<typeof meta>
export const Image: Story = {}
export const Empty: Story = {
  args: { image: false, title: 'Raw Silk Tunic — Natural / M' },
}
export const Collection: Story = {
  args: { image: false, title: 'Autumn Objects' },
}
export const Dark: Story = { args: { theme: 'dark' } }
export const ReducedMotion: Story = { args: { reduced: true } }
export const KeyboardFocus: Story = {
  play: async ({ canvasElement }) => {
    within(canvasElement).getByRole('link').focus()
    await waitFor(() =>
      expect(canvasElement.querySelector('[data-card-backdrop]')).toHaveStyle({
        opacity: '1',
      }),
    )
  },
}
