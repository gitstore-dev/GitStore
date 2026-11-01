import type { Meta, StoryObj } from '@storybook/react-vite'
import { ActionButton, ThemePicker } from '../components/controls'
import { ProfileMenu } from '../components/ProfileMenu'
import { NamespacePicker } from '../components/NamespacePicker'
import { PreferencesProvider } from '../state/preferences'
import type { Theme, MotionPreference } from '../state/preferences'
import { useState } from 'react'
import type { NamespaceId } from '../data/demo'
import { ArrowUpRight } from 'lucide-react'
function ComponentWorkbench({
  theme = 'light',
  motion = 'system',
  compact = false,
}: {
  theme?: Theme
  motion?: MotionPreference
  compact?: boolean
}) {
  const [namespace, setNamespace] = useState<NamespaceId>('atelier-kyoto')
  return (
    <PreferencesProvider initialTheme={theme} initialMotion={motion}>
      <div
        style={{
          padding: 36,
          background: 'var(--canvas)',
          color: 'var(--ink)',
          minHeight: '100vh',
          display: 'grid',
          gap: 32,
        }}
      >
        <h1 style={{ fontFamily: 'var(--display)', fontWeight: 500 }}>
          GitStore / shared controls
        </h1>
        <section
          aria-label="Button variants"
          style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}
        >
          <ActionButton variant="primary">
            Primary action <ArrowUpRight size={15} aria-hidden="true" />
          </ActionButton>
          <ActionButton>Secondary action</ActionButton>
          <ActionButton variant="ghost">Ghost action</ActionButton>
          <ActionButton isDisabled>Unavailable</ActionButton>
        </section>
        <section aria-label="Theme picker" style={{ maxWidth: 300 }}>
          <h2>Appearance</h2>
          <ThemePicker />
        </section>
        <section aria-label="Namespace selector" style={{ maxWidth: 300 }}>
          <h2>Workspace</h2>
          <NamespacePicker
            value={namespace}
            onChange={setNamespace}
            compact={compact}
          />
        </section>
        <section aria-label="Profile control" style={{ maxWidth: 300 }}>
          <h2>Profile</h2>
          <ProfileMenu compact={compact} />
        </section>
      </div>
    </PreferencesProvider>
  )
}
const meta = {
  title: 'Foundation/Controls',
  component: ComponentWorkbench,
  tags: ['autodocs'],
  args: { theme: 'light', motion: 'system', compact: false },
} satisfies Meta<typeof ComponentWorkbench>
export default meta
type Story = StoryObj<typeof meta>
export const Light: Story = {}
export const Dark: Story = { args: { theme: 'dark' } }
export const Compact: Story = { args: { compact: true } }
export const ReducedMotion: Story = { args: { motion: 'reduced' } }
