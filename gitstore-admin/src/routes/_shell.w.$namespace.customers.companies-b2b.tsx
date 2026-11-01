import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/customers/companies-b2b',
)({
  component: () => <PlaceholderPage destinationId="customers.companies-b2b" />,
})
