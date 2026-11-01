import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/inventory/stock-overview',
)({
  component: () => <PlaceholderPage destinationId="inventory.stock-overview" />,
})
