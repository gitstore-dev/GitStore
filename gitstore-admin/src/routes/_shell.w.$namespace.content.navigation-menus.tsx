import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/content/navigation-menus',
)({
  component: () => <PlaceholderPage destinationId="content.navigation-menus" />,
})
