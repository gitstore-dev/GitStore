import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/pricing-promotions/gift-cards',
)({
  component: () => (
    <PlaceholderPage destinationId="pricing-promotions.gift-cards" />
  ),
})
