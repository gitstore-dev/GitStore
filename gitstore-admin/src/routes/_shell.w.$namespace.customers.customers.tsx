import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/customers/customers',
)({ component: () => <PlaceholderPage destinationId="customers.customers" /> })
