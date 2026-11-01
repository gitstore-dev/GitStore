import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/settings/taxes-duties',
)({
  component: () => <PlaceholderPage destinationId="settings.taxes-duties" />,
})
