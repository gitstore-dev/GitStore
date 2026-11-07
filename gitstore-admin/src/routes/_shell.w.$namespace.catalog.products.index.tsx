import { createFileRoute } from '@tanstack/react-router'
import { ProductListPage } from '../features/catalog/products/ProductListPage'
import { isDemoNamespace } from '../data/demo'
export const Route = createFileRoute('/_shell/w/$namespace/catalog/products/')({
  component: Page,
})
function Page() {
  const { namespace } = Route.useParams()
  return isDemoNamespace(namespace) ? (
    <ProductListPage namespace={namespace} />
  ) : null
}
