import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/apps-automation/runs',
)({ component: () => <PlaceholderPage destinationId="apps-automation.runs" /> })
