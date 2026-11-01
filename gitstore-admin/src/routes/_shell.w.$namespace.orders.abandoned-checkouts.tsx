import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/orders/abandoned-checkouts',
)({
  component: () => (
    <PlaceholderPage destinationId="orders.abandoned-checkouts" />
  ),
})
