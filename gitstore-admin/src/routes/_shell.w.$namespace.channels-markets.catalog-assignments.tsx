import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/channels-markets/catalog-assignments',
)({
  component: () => (
    <PlaceholderPage destinationId="channels-markets.catalog-assignments" />
  ),
})
