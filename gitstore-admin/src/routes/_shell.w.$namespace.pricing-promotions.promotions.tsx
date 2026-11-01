import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/pricing-promotions/promotions',
)({
  component: () => (
    <PlaceholderPage destinationId="pricing-promotions.promotions" />
  ),
})
