import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/settings/team-access',
)({ component: () => <PlaceholderPage destinationId="settings.team-access" /> })
