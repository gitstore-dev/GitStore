import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute('/_shell/platform/resource-explorer')({
  component: () => (
    <PlaceholderPage destinationId="control-plane.resource-explorer" />
  ),
})
