import { createFileRoute } from '@tanstack/react-router'
import { OverviewPage } from '../features/home/overview/OverviewPage'
import { useHomeDemo } from '../features/demo/HomeDemoProvider'
import { isDemoNamespace } from '../data/demo'
export const Route = createFileRoute('/_shell/w/$namespace/home/overview')({
  component: OverviewRoute,
})
function OverviewRoute() {
  const { namespace } = Route.useParams()
  const epoch = useHomeDemo(
    isDemoNamespace(namespace) ? namespace : 'atelier-kyoto',
  ).session.epoch
  const { overviewScenario, overviewLighting } = Route.useRouteContext()
  return isDemoNamespace(namespace) ? (
    <OverviewPage
      key={`${namespace}:${epoch}:${overviewScenario ?? 'default'}`}
      namespace={namespace}
      scenario={overviewScenario}
      lighting={overviewLighting}
    />
  ) : null
}
