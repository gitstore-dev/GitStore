import { useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within } from 'storybook/test'
import { TagInput } from '../components/TagInput'
import { PreferencesProvider } from '../state/preferences'
import type { Theme } from '../state/preferences'
function TagWorkbench({
  initial = ['ceramics', 'homeware'],
  theme = 'light',
}: {
  initial?: readonly string[]
  theme?: Theme
}) {
  const [tags, setTags] = useState(initial)
  return (
    <PreferencesProvider initialTheme={theme}>
      <div
        style={{
          padding: 24,
          maxWidth: 540,
          background: 'var(--surface)',
          color: 'var(--ink)',
        }}
      >
        <TagInput value={tags} onChange={setTags} />
      </div>
    </PreferencesProvider>
  )
}
const meta = {
  title: 'Foundation/Tag input',
  component: TagWorkbench,
  render: (args) => <TagWorkbench key={JSON.stringify(args)} {...args} />,
} satisfies Meta<typeof TagWorkbench>
export default meta
type Story = StoryObj<typeof meta>
export const Populated: Story = {}
export const Empty: Story = { args: { initial: [] } }
export const Dark: Story = { args: { theme: 'dark' } }
export const LongTags: Story = {
  args: {
    initial: [
      'a-very-long-tag-that-should-wrap-within-the-input-area-without-overflow',
      'hand made',
    ],
  },
}
export const Keyboard: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.type(
      canvas.getByRole('textbox', { name: 'Add tag' }),
      'handmade{Enter}',
    )
    await expect(
      await canvas.findByRole('button', { name: 'Remove tag handmade' }),
    ).toBeVisible()
    await userEvent.click(
      await canvas.findByRole('button', { name: 'Remove tag handmade' }),
    )
    await expect(
      canvas.queryByRole('button', { name: 'Remove tag handmade' }),
    ).not.toBeInTheDocument()
  },
}
