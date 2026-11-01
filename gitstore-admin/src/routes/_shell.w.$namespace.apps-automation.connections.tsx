import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/apps-automation/connections',
)({
  component: () => (
    <PlaceholderPage destinationId="apps-automation.connections" />
  ),
})
