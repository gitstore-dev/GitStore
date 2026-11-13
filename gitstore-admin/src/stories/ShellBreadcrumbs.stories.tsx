import { useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { ShellBreadcrumbs } from '../shell/ShellBreadcrumbs'
import { PreferencesProvider } from '../state/preferences'
function Preview({
  width = 700,
  current = 'Ceramic Everyday Cup',
  dark = false,
}: {
  width?: number
  current?: string
  dark?: boolean
}) {
  const [selected, setSelected] = useState('')
  return (
    <PreferencesProvider initialTheme={dark ? 'dark' : 'light'}>
      <div
        style={{
          padding: 24,
          background: 'var(--canvas)',
          color: 'var(--ink)',
          minHeight: 240,
        }}
      >
        <div
          data-breadcrumb-preview=""
          style={{ display: 'flex', width, maxWidth: '100%' }}
        >
          <ShellBreadcrumbs
            current={current}
            ancestors={['Catalog', 'Products'].map((label) => ({
              label,
              link: (
                <a
                  href={`#${label}`}
                  onClick={(event) => {
                    event.preventDefault()
                    setSelected(label)
                  }}
                >
                  {label}
                </a>
              ),
              onSelect: () => setSelected(label),
            }))}
          />
        </div>
        <p role="status">{selected ? `Selected ${selected}` : ''}</p>
      </div>
    </PreferencesProvider>
  )
}
const meta = {
  title: 'Foundation/Shell breadcrumbs',
  component: Preview,
} satisfies Meta<typeof Preview>
export default meta
type Story = StoryObj<typeof meta>
export const Standard: Story = {}
export const LongTitle: Story = {
  args: {
    width: 400,
    current:
      'Ceramic Everyday Cup with a hand-finished glaze for everyday gatherings',
  },
}
export const Collapsed: Story = { args: { width: 200 } }
export const Dark: Story = { args: { width: 200, dark: true } }
export const Mobile: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const MaximumTitle: Story = {
  args: { width: 240, current: 'W'.repeat(200) },
}
