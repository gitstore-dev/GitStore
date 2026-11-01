import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/changes-releases/review-queue',
)({
  component: () => (
    <PlaceholderPage destinationId="changes-releases.review-queue" />
  ),
})
