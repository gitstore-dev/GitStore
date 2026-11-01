import { createFileRoute } from '@tanstack/react-router'
import { PlaceholderPage } from '../pages/PlaceholderPage'
export const Route = createFileRoute('/_shell/w/$namespace/home/my-work')({
  component: () => <PlaceholderPage destinationId="home.my-work" />,
})
