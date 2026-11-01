import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute('/_shell/platform/system-health')({
  component: () => (
    <PlaceholderPage destinationId="control-plane.system-health" />
  ),
})
