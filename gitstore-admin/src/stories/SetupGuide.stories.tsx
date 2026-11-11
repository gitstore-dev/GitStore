import { useState } from 'react'
import { RouterProvider } from '@tanstack/react-router'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { expect, userEvent, within, waitFor } from 'storybook/test'
import { createAppRouter } from '../app/router'
import type { NamespaceId } from '../data/demo'
import type { Theme, MotionPreference } from '../state/preferences'
import type {
  SetupScenario,
  SetupStepId,
} from '../features/home/setup-guide/model'
function SetupPreview({
  namespace = 'atelier-kyoto',
  scenario,
  step,
  theme = 'light',
  motion = 'system',
}: {
  namespace?: NamespaceId
  scenario?: SetupScenario
  step?: SetupStepId
  theme?: Theme
  motion?: MotionPreference
}) {
  const [router] = useState(() =>
    createAppRouter({
      initialPath: `/w/${namespace}/home/setup-guide${step ? `?step=${step}` : ''}`,
      context: {
        setupScenario: scenario,
        preferences: { initialTheme: theme, initialMotion: motion },
      },
    }),
  )
  return <RouterProvider router={router} />
}
const meta = {
  title: 'Pages/Home/Setup guide',
  component: SetupPreview,
  parameters: { layout: 'fullscreen' },
  args: { namespace: 'atelier-kyoto', theme: 'light' },
  render: (args) => <SetupPreview key={JSON.stringify(args)} {...args} />,
} satisfies Meta<typeof SetupPreview>
export default meta
type Story = StoryObj<typeof meta>
export const InitialKyoto: Story = {}
export const FirstUseBerlin: Story = { args: { namespace: 'studio-berlin' } }
export const PartialProgress: Story = {
  args: { namespace: 'studio-berlin', scenario: 'partial' },
}
export const Complete: Story = { args: { scenario: 'complete' } }
export const ReopenedReview: Story = {
  args: { scenario: 'reopened', step: 'categories' },
}
export const Inspector: Story = { args: { step: 'catalog-checks' } }
export const Confirmation: Story = {
  args: { step: 'catalog-checks' },
  play: async ({ canvasElement }) => {
    const body = within(canvasElement.ownerDocument.body)
    await userEvent.click(
      await body.findByRole(
        'button',
        { name: 'Mark reviewed in demo' },
        { timeout: 10000 },
      ),
    )
    await expect(
      await body.findByRole('dialog', {
        name: 'Mark this step reviewed in demo?',
      }),
    ).toBeVisible()
  },
}
export const Reviewed: Story = {
  args: { step: 'catalog-checks' },
  play: async ({ canvasElement }) => {
    const body = within(canvasElement.ownerDocument.body)
    await userEvent.click(
      await body.findByRole(
        'button',
        { name: 'Mark reviewed in demo' },
        { timeout: 10000 },
      ),
    )
    await userEvent.click(
      await body.findByRole('button', { name: 'Confirm demo review' }),
    )
    await waitFor(() =>
      expect(
        body.getByRole('heading', { name: 'Review catalog checks' }),
      ).toHaveFocus(),
    )
    await expect(
      body.getByRole('progressbar', { name: 'Demo setup progress' }),
    ).toHaveAttribute('value', '4')
  },
}
export const Loading: Story = { args: { scenario: 'loading' } }
export const Unavailable: Story = { args: { scenario: 'unavailable' } }
export const ReadOnly: Story = {
  args: { scenario: 'read-only', step: 'catalog-checks' },
}
export const Dark: Story = { args: { theme: 'dark' } }
export const Mobile: Story = {
  args: { namespace: 'studio-berlin' },
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
export const ReducedMotion: Story = { args: { motion: 'reduced' } }
export const ProductDeleting: Story = {
  args: { namespace: 'studio-berlin', step: 'first-product' },
  play: async ({ canvasElement }) => {
    const body = within(canvasElement.ownerDocument.body)
    await userEvent.click(
      await body.findByRole(
        'link',
        { name: /Add demo product/ },
        { timeout: 10000 },
      ),
    )
    await waitFor(() =>
      expect(canvasElement.querySelector('main h1')).toHaveTextContent(
        'New product',
      ),
    )
    const modalInspector = body.queryByRole('dialog', { name: 'Page context' })
    if (modalInspector) {
      await userEvent.click(
        within(modalInspector).getByRole('button', { name: 'Close inspector' }),
      )
      await waitFor(() =>
        expect(
          body.queryByRole('dialog', { name: 'Page context' }),
        ).not.toBeInTheDocument(),
      )
    }
    await userEvent.type(
      await body.findByRole('textbox', { name: 'Title' }, { timeout: 10000 }),
      'Setup sample product',
    )
    await userEvent.click(
      body.getByRole('button', { name: 'Create demo product' }),
    )
    await body.findByRole(
      'heading',
      { name: 'Setup sample product', level: 1 },
      { timeout: 10000 },
    )
    await userEvent.click(
      await body.findByRole(
        'button',
        { name: 'Delete demo product' },
        { timeout: 10000 },
      ),
    )
    await userEvent.click(
      await body.findByRole('button', { name: 'Confirm demo deletion' }),
    )
    await userEvent.keyboard('{Control>}k{/Control}')
    await userEvent.type(
      await body.findByRole('searchbox', { name: 'Search pages' }),
      'Setup guide',
    )
    await userEvent.click(
      await body.findByRole('option', { name: /Setup guide/ }),
    )
    await expect(
      await body.findByRole(
        'progressbar',
        { name: 'Demo setup progress' },
        { timeout: 10000 },
      ),
    ).toHaveAttribute('value', '0')
  },
}
