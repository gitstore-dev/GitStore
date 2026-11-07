import { createFileRoute, useParams } from '@tanstack/react-router'
import { AppShell } from '../shell/AppShell'
import { usePreferences } from '../state/preferences'
import { isDemoNamespace } from '../data/demo'
import { useHomeDemo } from '../features/demo/HomeDemoProvider'
export const Route = createFileRoute('/_shell')({ component: SessionShell })
function SessionShell() {
  const params = useParams({ strict: false })
  const preferences = usePreferences()
  const namespace =
    'namespace' in params &&
    params.namespace &&
    isDemoNamespace(params.namespace)
      ? params.namespace
      : preferences.namespace
  const { session } = useHomeDemo(namespace)
  return <AppShell key={`${namespace}:${session.epoch}`} />
}
