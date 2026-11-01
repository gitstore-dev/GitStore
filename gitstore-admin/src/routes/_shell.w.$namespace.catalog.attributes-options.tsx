import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/attributes-options',
)({
  component: () => (
    <PlaceholderPage destinationId="catalog.attributes-options" />
  ),
})
