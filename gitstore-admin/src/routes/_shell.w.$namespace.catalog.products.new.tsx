import { createFileRoute } from '@tanstack/react-router'
import { ProductPage } from '../features/catalog/products/ProductPage'
import { isDemoNamespace } from '../data/demo'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/products/new',
)({ component: Page })
function Page() {
  const { namespace } = Route.useParams()
  return isDemoNamespace(namespace) ? (
    <ProductPage namespace={namespace} editing />
  ) : null
}
