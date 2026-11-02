import { useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { LayoutPicker } from '../shell/LayoutPicker'
import type { LayoutMode } from '../shell/WorkspaceLayout'
import { PreferencesProvider } from '../state/preferences'

function Preview({
  initial = 'standard',
  dark = false,
}: {
  initial?: LayoutMode
  dark?: boolean
}) {
  const [mode, setMode] = useState(initial)
  return (
    <PreferencesProvider initialTheme={dark ? 'dark' : 'light'}>
      <div
        style={{
          padding: 32,
          background: 'var(--canvas)',
          color: 'var(--ink)',
        }}
      >
        <LayoutPicker value={mode} onChange={setMode} />
      </div>
    </PreferencesProvider>
  )
}

const meta = {
  title: 'Foundation/Layout picker',
  component: Preview,
} satisfies Meta<typeof Preview>
export default meta
type Story = StoryObj<typeof meta>
export const Standard: Story = {}
export const Inspector: Story = { args: { initial: 'inspector' } }
export const Focus: Story = { args: { initial: 'focus' } }
export const Dark: Story = { args: { dark: true } }
