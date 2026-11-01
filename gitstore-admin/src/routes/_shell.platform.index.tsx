import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/platform/')({
  beforeLoad: () => {
    throw redirect({ to: '/platform/namespaces' })
  },
})
