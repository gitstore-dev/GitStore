import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute('/_shell/platform/reconciliation-health')({
  component: () => (
    <PlaceholderPage destinationId="control-plane.reconciliation-health" />
  ),
})
