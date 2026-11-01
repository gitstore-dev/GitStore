import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/orders/draft-orders',
)({ component: () => <PlaceholderPage destinationId="orders.draft-orders" /> })
