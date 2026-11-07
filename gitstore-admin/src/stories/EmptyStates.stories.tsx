import type { Meta, StoryObj } from '@storybook/react-vite'
import { EmptyState } from '../features/home/shared/EmptyState'
import { PreferencesProvider } from '../state/preferences'
import { ActionButton } from '../components/controls'
function EmptyStatePreview({
  title,
  description,
  action,
}: {
  title: string
  description: string
  action?: string
}) {
  return (
    <PreferencesProvider>
      <div
        style={{
          padding: 20,
          background: 'var(--canvas)',
          color: 'var(--ink)',
        }}
      >
        <EmptyState
          title={title}
          actions={action ? <ActionButton>{action}</ActionButton> : undefined}
        >
          {description}
        </EmptyState>
      </div>
    </PreferencesProvider>
  )
}
const meta = {
  title: 'Foundation/Home empty states',
  component: EmptyStatePreview,
  args: {
    title: 'Your catalog starts here',
    description: 'Set up your store and add your first product.',
  },
} satisfies Meta<typeof EmptyStatePreview>
export default meta
type Story = StoryObj<typeof meta>
export const Catalog: Story = {}
export const MyWork: Story = {
  args: {
    title: 'No work yet',
    description: 'Your drafts and assigned reviews will appear here.',
  },
}
export const Activity: Story = {
  args: {
    title: 'No activity yet',
    description: 'Changes and reviews will appear here as you work.',
  },
}
export const NoResults: Story = {
  args: {
    title: 'No matching activity',
    description:
      'Try another title, actor, item type, event category, or date.',
    action: 'Clear filters',
  },
}
export const Mobile: Story = {
  ...Activity,
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
