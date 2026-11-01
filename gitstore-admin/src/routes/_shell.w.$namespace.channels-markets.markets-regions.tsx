import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/channels-markets/markets-regions',
)({
  component: () => (
    <PlaceholderPage destinationId="channels-markets.markets-regions" />
  ),
})
