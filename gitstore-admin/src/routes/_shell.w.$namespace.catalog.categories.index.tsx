import { createFileRoute } from '@tanstack/react-router'
import { isDemoNamespace } from '../data/demo'
import { CatalogListPage } from '../features/catalog/browse/CatalogListPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/categories/',
)({ component: Page })
function Page() {
  const { namespace } = Route.useParams()
  return isDemoNamespace(namespace) ? (
    <CatalogListPage namespace={namespace} domain="categories" />
  ) : null
}
