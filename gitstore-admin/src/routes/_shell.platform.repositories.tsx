import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute('/_shell/platform/repositories')({
  component: () => (
    <PlaceholderPage destinationId="control-plane.repositories" />
  ),
})
