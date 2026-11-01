import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/customers/groups-segments',
)({
  component: () => (
    <PlaceholderPage destinationId="customers.groups-segments" />
  ),
})
