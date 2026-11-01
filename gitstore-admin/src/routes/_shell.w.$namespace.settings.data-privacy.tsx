import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/settings/data-privacy',
)({
  component: () => <PlaceholderPage destinationId="settings.data-privacy" />,
})
