import { createFileRoute } from '@tanstack/react-router'
import { MyWorkPage } from '../features/home/my-work/MyWorkPage'
import { isDemoNamespace } from '../data/demo'
export const Route = createFileRoute('/_shell/w/$namespace/home/my-work')({
  component: MyWorkRoute,
})
function MyWorkRoute() {
  const { namespace } = Route.useParams()
  const { myWorkScenario, myWorkTab } = Route.useRouteContext()
  return isDemoNamespace(namespace) ? (
    <MyWorkPage
      key={`${namespace}:${myWorkScenario ?? 'default'}:${myWorkTab ?? 'drafts'}`}
      namespace={namespace}
      scenario={myWorkScenario}
      initialTab={myWorkTab}
    />
  ) : null
}
