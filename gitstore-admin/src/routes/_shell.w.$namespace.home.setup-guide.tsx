import { createFileRoute } from '@tanstack/react-router'
import { isDemoNamespace } from '../data/demo'
import { SetupGuidePage } from '../features/home/setup-guide/SetupGuidePage'
import { setupSearch } from '../features/home/setup-guide/model'
export const Route = createFileRoute('/_shell/w/$namespace/home/setup-guide')({
  validateSearch: setupSearch,
  component: Page,
})
function Page() {
  const { namespace } = Route.useParams()
  const { step } = Route.useSearch()
  const { setupScenario } = Route.useRouteContext()
  return isDemoNamespace(namespace) ? (
    <SetupGuidePage
      namespace={namespace}
      step={step}
      scenario={setupScenario}
    />
  ) : null
}
