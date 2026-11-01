import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute('/_shell/w/$namespace/insights/products')({
  component: () => <PlaceholderPage destinationId="insights.products" />,
})
