import { useState } from 'react'
import { RouterProvider } from '@tanstack/react-router'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { userEvent, within } from 'storybook/test'
import { createAppRouter } from '../app/router'
import type { OverviewScenario } from '../features/home/overview/model'
import type { NamespaceId } from '../data/demo'
import type { Theme, MotionPreference } from '../state/preferences'
import type { LayoutMode } from '../shell/WorkspaceLayout'
function OverviewPreview({
  namespace = 'atelier-kyoto',
  scenario,
  theme = 'light',
  motion = 'system',
  layout = 'standard',
  lighting = 'auto',
}: {
  namespace?: NamespaceId
  scenario?: OverviewScenario
  theme?: Theme
  motion?: MotionPreference
  layout?: LayoutMode
  lighting?: 'auto' | 'css'
}) {
  const [router] = useState(() =>
    createAppRouter({
      initialPath: `/w/${namespace}/home/overview`,
      context: {
        overviewScenario: scenario,
        overviewLighting: lighting,
        initialLayout: layout,
        preferences: { initialTheme: theme, initialMotion: motion },
      },
    }),
  )
  return <RouterProvider router={router} />
}
const meta = {
  title: 'Pages/Home/Overview',
  component: OverviewPreview,
  parameters: { layout: 'fullscreen' },
  args: {
    namespace: 'atelier-kyoto',
    theme: 'light',
    motion: 'system',
    layout: 'standard',
  },
  render: (args) => <OverviewPreview key={JSON.stringify(args)} {...args} />,
} satisfies Meta<typeof OverviewPreview>
export default meta
type Story = StoryObj<typeof meta>
export const Populated: Story = {}
export const Dark: Story = { args: { theme: 'dark' } }
export const FirstUse: Story = { args: { namespace: 'studio-berlin' } }
export const AllClear: Story = { args: { scenario: 'all-clear' } }
export const FilteredEmpty: Story = { args: { scenario: 'filtered-empty' } }
export const Loading: Story = { args: { scenario: 'loading' } }
export const Unavailable: Story = { args: { scenario: 'unavailable' } }
export const Stale: Story = { args: { scenario: 'stale' } }
export const Restricted: Story = { args: { scenario: 'restricted' } }
export const Focus: Story = { args: { layout: 'focus' } }
export const ReducedMotion: Story = { args: { motion: 'reduced' } }
export const Tablet: Story = {
  globals: { viewport: { value: 'tablet', isRotated: false } },
}
export const Mobile: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const Inspector: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole('button', {
        name: 'View details for Kanso Stone Basin',
      }),
    )
  },
}
export const Confirmation: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole('button', {
        name: 'View details for Kanso Stone Basin',
      }),
    )
    await userEvent.click(
      await within(canvasElement.ownerDocument.body).findByRole('button', {
        name: 'Apply demo fix',
      }),
    )
  },
}

/** Interactive lighting specimen: focus or hover any readiness card. */
export const StateLighting: Story = {
  args: { theme: 'dark' },
  parameters: {
    docs: {
      description: {
        story:
          'One shared WebGPU light field when available; CSS edges and halos otherwise. Hover or keyboard-focus a card. Labels and icons carry readiness independently of lighting.',
      },
    },
  },
}
export const FilterChoreography: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(canvas.getByRole('radio', { name: /Needs review/ }))
  },
  parameters: {
    docs: {
      description: {
        story:
          'Clear filters, change the status, or sort by name to see cards move between positions. Exits are decorative, inert copies; the live cards remain HTML.',
      },
    },
  },
}
export const ReducedMotionFiltering: Story = {
  args: { motion: 'reduced' },
  play: FilterChoreography.play,
}

export const CssStateLighting: Story = {
  args: { theme: 'dark', lighting: 'css' },
  parameters: {
    docs: {
      description: {
        story:
          'Explicit CSS-only lighting specimen: no WebGPU adapter or canvas is requested. Readiness labels, edges, and focus/hover lighting remain available.',
      },
    },
  },
}
