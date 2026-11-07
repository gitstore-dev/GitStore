import { createFileRoute } from '@tanstack/react-router'
import { ProductPage } from '../features/catalog/products/ProductPage'
import { isDemoNamespace } from '../data/demo'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/products/$productId/edit',
)({ component: Page })
function Page() {
  const { namespace, productId } = Route.useParams()
  return isDemoNamespace(namespace) ? (
    <ProductPage
      key={productId + 'edit'}
      namespace={namespace}
      productId={productId}
      editing={true}
    />
  ) : null
}
