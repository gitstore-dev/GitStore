import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/content/structured-content',
)({
  component: () => (
    <PlaceholderPage destinationId="content.structured-content" />
  ),
})
