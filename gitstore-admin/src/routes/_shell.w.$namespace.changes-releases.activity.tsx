import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/changes-releases/activity',
)({
  component: () => (
    <PlaceholderPage destinationId="changes-releases.activity" />
  ),
})
