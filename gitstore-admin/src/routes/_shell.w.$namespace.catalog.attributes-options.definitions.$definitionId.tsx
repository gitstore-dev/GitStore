import { createFileRoute } from '@tanstack/react-router'
import { isDemoNamespace } from '../data/demo'
import { StructureDetailPage } from '../features/catalog/structure/StructureDetailPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/attributes-options/definitions/$definitionId',
)({ component: Page })
function Page() {
  const { namespace } = Route.useParams()
  return isDemoNamespace(namespace) ? (
    <StructureDetailPage namespace={namespace} />
  ) : null
}
