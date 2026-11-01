import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/insights/saved-reports',
)({
  component: () => <PlaceholderPage destinationId="insights.saved-reports" />,
})
