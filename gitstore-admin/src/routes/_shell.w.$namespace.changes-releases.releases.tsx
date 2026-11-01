import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/changes-releases/releases',
)({
  component: () => (
    <PlaceholderPage destinationId="changes-releases.releases" />
  ),
})
