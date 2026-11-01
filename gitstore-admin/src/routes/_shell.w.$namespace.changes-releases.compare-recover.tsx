import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/changes-releases/compare-recover',
)({
  component: () => (
    <PlaceholderPage destinationId="changes-releases.compare-recover" />
  ),
})
