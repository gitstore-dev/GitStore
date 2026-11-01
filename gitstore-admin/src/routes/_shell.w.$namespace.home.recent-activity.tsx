import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/home/recent-activity',
)({ component: () => <PlaceholderPage destinationId="home.recent-activity" /> })
