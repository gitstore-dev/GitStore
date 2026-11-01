import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute('/_shell/w/$namespace/orders/all-orders')({
  component: () => <PlaceholderPage destinationId="orders.all-orders" />,
})
