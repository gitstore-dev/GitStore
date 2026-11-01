import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/inventory/incoming-stock',
)({
  component: () => <PlaceholderPage destinationId="inventory.incoming-stock" />,
})
