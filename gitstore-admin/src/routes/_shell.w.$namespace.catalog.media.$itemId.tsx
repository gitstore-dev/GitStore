import { createFileRoute } from '@tanstack/react-router'
import { isDemoNamespace } from '../data/demo'
import { MediaDetailPage } from '../features/catalog/media/MediaDetailPage'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/media/$itemId',
)({ component: Page })
function Page() {
  const { namespace, itemId } = Route.useParams()
  return isDemoNamespace(namespace) ? (
    <MediaDetailPage namespace={namespace} itemId={itemId} />
  ) : null
}
