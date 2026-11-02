import { useState } from 'react'
import { RouterProvider } from '@tanstack/react-router'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within } from 'storybook/test'
import { createAppRouter } from '../app/router'
import type { LayoutMode } from '../shell/WorkspaceLayout'
import type { Theme, MotionPreference } from '../state/preferences'
function ShellPreview({
  theme = 'light',
  mode = 'standard',
  collapsed = false,
  motion = 'system',
  path = '/w/atelier-kyoto/home/overview',
}: {
  theme?: Theme
  mode?: LayoutMode
  collapsed?: boolean
  motion?: MotionPreference
  path?: string
}) {
  const [router] = useState(() =>
    createAppRouter({
      initialPath: path,
      context: {
        initialLayout: mode,
        preferences: {
          initialTheme: theme,
          initialMotion: motion,
          initialCollapsed: collapsed,
        },
      },
    }),
  )
  return <RouterProvider router={router} />
}
const meta = {
  title: 'Foundation/Layouts',
  component: ShellPreview,
  tags: ['autodocs'],
  args: {
    theme: 'light',
    mode: 'standard',
    collapsed: false,
    motion: 'system',
    path: '/w/atelier-kyoto/home/overview',
  },
  parameters: { layout: 'fullscreen' },
} satisfies Meta<typeof ShellPreview>
export default meta
type Story = StoryObj<typeof meta>
export const Desktop: Story = {}
export const DarkDesktop: Story = { args: { theme: 'dark' } }
export const CollapsedRail: Story = { args: { collapsed: true } }
export const WithInspector: Story = {
  args: { mode: 'inspector', path: '/w/atelier-kyoto/catalog/products' },
}
export const FocusMode: Story = { args: { mode: 'focus' } }
export const FocusTransition: Story = {
  play: async ({ canvasElement }) => {
    const screen = within(canvasElement)
    await userEvent.click(screen.getByRole('button', { name: 'Focus mode' }))
    await expect(
      await screen.findByRole('button', { name: 'Exit Focus Mode' }),
    ).toBeVisible()
  },
}
export const FocusTransitionDark: Story = {
  ...FocusTransition,
  args: { theme: 'dark' },
}
export const FocusTransitionReducedMotion: Story = {
  ...FocusTransition,
  args: { motion: 'reduced' },
}
export const ReducedMotion: Story = { args: { motion: 'reduced' } }
export const PlatformScope: Story = { args: { path: '/platform/namespaces' } }
export const SettingsContext: Story = {
  args: { path: '/w/studio-berlin/settings/store-details' },
}
export const CommerceContext: Story = {
  args: { path: '/w/studio-berlin/catalog/products' },
}
export const OperationsContext: Story = {
  args: { path: '/w/studio-berlin/changes-releases/my-changes' },
}
export const ContentContext: Story = {
  args: { path: '/w/studio-berlin/content/pages' },
}
export const ContextRail: Story = {
  args: { collapsed: true, path: '/w/studio-berlin/catalog/products' },
}
export const SettingsMobile: Story = {
  args: { path: '/w/studio-berlin/settings/store-details' },
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const Tablet: Story = {
  globals: { viewport: { value: 'tablet', isRotated: false } },
}
export const Mobile: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const MobileCatalog: Story = {
  args: { path: '/w/studio-berlin/catalog/products' },
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const MobilePlatform: Story = {
  args: { path: '/platform/policies' },
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const ContextSelectorOpen: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole('button', { name: /Workspace context/ }),
    )
  },
}
export const PlatformSelectorOpen: Story = {
  ...ContextSelectorOpen,
  args: { path: '/platform/namespaces' },
}
export const ProfileMenuOpen: Story = {
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole('button', { name: /Elena Rostova/ }),
    )
  },
}
export const MobileDrawerOpen: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole('button', { name: 'Open navigation' }),
    )
  },
}
export const MobileProfileMenuOpen: Story = {
  args: { path: '/w/studio-berlin/settings/store-details' },
  globals: { viewport: { value: 'mobile1', isRotated: false } },
  play: async ({ canvasElement }) => {
    await userEvent.click(
      within(canvasElement).getByRole('button', {
        name: 'Open navigation menu',
      }),
    )
    const drawer = await within(canvasElement.ownerDocument.body).findByRole(
      'dialog',
      { name: 'Mobile navigation' },
    )
    await userEvent.click(
      within(drawer).getByRole('button', { name: /Elena Rostova/ }),
    )
  },
}
