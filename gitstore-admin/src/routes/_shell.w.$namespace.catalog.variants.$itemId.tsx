import { createFileRoute } from '@tanstack/react-router'
import { isDemoNamespace } from '../data/demo'
import { CatalogDetailPage } from '../features/catalog/browse/CatalogDetailPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/variants/$itemId',
)({ component: Page })
function Page() {
  const { namespace, itemId } = Route.useParams()
  return isDemoNamespace(namespace) ? (
    <CatalogDetailPage
      namespace={namespace}
      domain="variants"
      itemId={itemId}
    />
  ) : null
}
