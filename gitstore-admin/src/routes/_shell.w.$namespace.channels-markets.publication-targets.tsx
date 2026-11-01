import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/channels-markets/publication-targets',
)({
  component: () => (
    <PlaceholderPage destinationId="channels-markets.publication-targets" />
  ),
})
