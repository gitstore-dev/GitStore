import { createFileRoute } from '@tanstack/react-router'
import { isDemoNamespace } from '../data/demo'
import { RecentActivityPage } from '../features/home/recent-activity/RecentActivityPage'
import { useHomeDemo } from '../features/demo/HomeDemoProvider'
export const Route = createFileRoute(
  '/_shell/w/$namespace/home/recent-activity',
)({ component: ActivityRoute })
function ActivityRoute() {
  const { namespace } = Route.useParams()
  const { activityScenario } = Route.useRouteContext()
  const epoch = useHomeDemo(
    isDemoNamespace(namespace) ? namespace : 'atelier-kyoto',
  ).session.epoch
  return isDemoNamespace(namespace) ? (
    <RecentActivityPage
      key={`${namespace}:${epoch}:${activityScenario ?? 'default'}`}
      namespace={namespace}
      scenario={activityScenario}
    />
  ) : null
}
