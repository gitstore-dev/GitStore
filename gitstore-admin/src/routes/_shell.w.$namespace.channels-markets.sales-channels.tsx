import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/channels-markets/sales-channels',
)({
  component: () => (
    <PlaceholderPage destinationId="channels-markets.sales-channels" />
  ),
})
