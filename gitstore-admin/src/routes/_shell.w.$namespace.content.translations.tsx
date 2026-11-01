import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/content/translations',
)({ component: () => <PlaceholderPage destinationId="content.translations" /> })
