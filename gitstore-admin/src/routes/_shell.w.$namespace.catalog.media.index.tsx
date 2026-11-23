import { createFileRoute } from '@tanstack/react-router'
import { isDemoNamespace } from '../data/demo'
import { MediaListPage } from '../features/catalog/media/MediaListPage'
export const Route = createFileRoute('/_shell/w/$namespace/catalog/media/')({
  component: Page,
})
function Page() {
  const { namespace } = Route.useParams()
  return isDemoNamespace(namespace) ? (
    <MediaListPage namespace={namespace} />
  ) : null
}
