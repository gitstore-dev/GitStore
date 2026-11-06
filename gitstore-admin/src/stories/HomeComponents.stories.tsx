import { useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { Boxes, CircleCheck, RefreshCw, TriangleAlert } from 'lucide-react'
import { PreferencesProvider } from '../state/preferences'
import type { Theme } from '../state/preferences'
import {
  HomePage,
  HomePageHeader,
  DemoNotice,
} from '../features/home/shared/HomePage'
import { ResourceCard, SummaryCard } from '../features/home/shared/HomeCards'
import styles from '../features/home/shared/home.module.css'
function HomeComponents({ theme = 'light' }: { theme?: Theme }) {
  const [message, setMessage] = useState('')
  return (
    <PreferencesProvider initialTheme={theme}>
      <HomePage>
        <HomePageHeader
          namespace="Atelier Kyoto"
          title="Overview"
          description="Your store at a glance."
          onReset={() => setMessage('Demo reset requested.')}
        />
        <DemoNotice page="Overview" />
        <section className={styles.metrics} aria-label="Summary card examples">
          <SummaryCard
            label="Total items"
            value={14}
            detail="Store content and configuration"
            Icon={Boxes}
            ratio={1}
          />
          <SummaryCard
            label="Needs attention"
            value={4}
            detail="Issues or outdated information"
            Icon={TriangleAlert}
            state="blocked"
            ratio={4 / 14}
          />
          <SummaryCard
            label="Processing"
            value={2}
            detail="Updates in progress in this demo"
            Icon={RefreshCw}
            state="reconciling"
            ratio={2 / 14}
          />
          <SummaryCard
            label="Ready"
            value={8}
            detail="Ready in this demo snapshot"
            Icon={CircleCheck}
            state="ready"
            ratio={8 / 14}
          />
        </section>
        <div className={styles.resourceGrid}>
          <ResourceCard
            id="card-example"
            title="Kanso Stone Basin"
            kind="Product"
            state="blocked"
            badge={<span>Needs review</span>}
            description="Add the missing shipping weight so this product can pass its checks."
            onInspect={() => setMessage('Item details requested.')}
          />
        </div>
        <p role="status">{message}</p>
      </HomePage>
    </PreferencesProvider>
  )
}
const meta = {
  title: 'Foundation/Home components',
  component: HomeComponents,
  parameters: { layout: 'fullscreen' },
  args: { theme: 'light' },
} satisfies Meta<typeof HomeComponents>
export default meta
type Story = StoryObj<typeof meta>
export const Light: Story = {}
export const Dark: Story = { args: { theme: 'dark' } }
export const Mobile: Story = {
  globals: { viewport: { value: 'mobile1', isRotated: false } },
}
