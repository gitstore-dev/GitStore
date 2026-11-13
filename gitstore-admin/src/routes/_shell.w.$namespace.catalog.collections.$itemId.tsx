import { createFileRoute } from '@tanstack/react-router'
import { isDemoNamespace } from '../data/demo'
import { CatalogDetailPage } from '../features/catalog/browse/CatalogDetailPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/collections/$itemId',
)({ component: Page })
function Page() {
  const { namespace, itemId } = Route.useParams()
  return isDemoNamespace(namespace) ? (
    <CatalogDetailPage
      namespace={namespace}
      domain="collections"
      itemId={itemId}
    />
  ) : null
}
