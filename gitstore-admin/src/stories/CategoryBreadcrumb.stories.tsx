import { useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within } from 'storybook/test'
import { CategoryBreadcrumb } from '../features/catalog/browse/CategoryBreadcrumb'
import { PreferencesProvider } from '../state/preferences'

function Preview({
  width = 900,
  deep = false,
  long = false,
  theme = 'light',
}: {
  width?: number
  deep?: boolean
  long?: boolean
  theme?: 'light' | 'dark'
}) {
  const [selected, setSelected] = useState('')
  const labels = deep
    ? [
        'Homeware',
        'Living',
        'Kitchen',
        'Dining',
        'Everyday objects',
        'Handmade',
        'Stoneware',
        'Ceramics',
        long
          ? 'Tableware for thoughtfully considered everyday gatherings and celebrations'
          : 'Tableware',
      ]
    : ['Homeware', 'Ceramics', 'Tableware']
  return (
    <PreferencesProvider initialTheme={theme}>
      <div
        style={{
          width,
          maxWidth: '100%',
          padding: 24,
          background: 'var(--surface)',
          color: 'var(--ink)',
          minHeight: 280,
        }}
      >
        <CategoryBreadcrumb
          items={labels.map((title, index) => ({
            id: String(index),
            title,
            link: (
              <a
                href={`#category-${index}`}
                title={title}
                onClick={(event) => {
                  event.preventDefault()
                  setSelected(title)
                }}
              >
                {title}
              </a>
            ),
            onSelect: () => setSelected(title),
          }))}
        />
        <p role="status">{selected ? `Selected ${selected}` : ''}</p>
      </div>
    </PreferencesProvider>
  )
}
const meta = {
  title: 'Foundation/Category path',
  component: Preview,
} satisfies Meta<typeof Preview>
export default meta
type Story = StoryObj<typeof meta>
export const Short: Story = {}
export const Deep: Story = {
  args: { deep: true, width: 600 },
  play: async ({ canvasElement }) => {
    const screen = within(canvasElement)
    const trigger = screen.getByRole('button', {
      name: 'Show omitted category ancestors',
    })
    await userEvent.click(trigger)
    const menu = within(canvasElement.ownerDocument.body).getByRole('menu', {
      name: 'Omitted category ancestors',
    })
    await expect(within(menu).getAllByRole('menuitem')[0]).toHaveTextContent(
      'Living',
    )
    await userEvent.keyboard('{Escape}')
    await expect(trigger).toHaveFocus()
  },
}
export const LongTitle: Story = { args: { deep: true, long: true, width: 380 } }
export const Dark: Story = { args: { deep: true, width: 600, theme: 'dark' } }
export const Mobile: Story = {
  args: { deep: true, width: 340 },
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
