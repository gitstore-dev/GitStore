import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute('/_shell/w/$namespace/home/setup-guide')({
  component: () => <PlaceholderPage destinationId="home.setup-guide" />,
})
