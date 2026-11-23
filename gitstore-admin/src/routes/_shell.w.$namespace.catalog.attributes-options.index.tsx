import { createFileRoute } from '@tanstack/react-router'
import { isDemoNamespace } from '../data/demo'
import { StructureListPage } from '../features/catalog/structure/StructureListPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/attributes-options/',
)({ component: Page })
function Page() {
  const { namespace } = Route.useParams()
  return isDemoNamespace(namespace) ? (
    <StructureListPage namespace={namespace} domain="attributes-options" />
  ) : null
}
