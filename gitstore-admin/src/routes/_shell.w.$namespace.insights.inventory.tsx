import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute('/_shell/w/$namespace/insights/inventory')(
  { component: () => <PlaceholderPage destinationId="insights.inventory" /> },
)
