import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute('/_shell/platform/namespaces')({
  component: () => <PlaceholderPage destinationId="control-plane.namespaces" />,
})
