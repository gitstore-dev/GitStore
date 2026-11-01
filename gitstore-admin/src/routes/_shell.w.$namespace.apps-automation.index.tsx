import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/apps-automation/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/apps-automation/installed-apps',
      params: { namespace: params.namespace },
    })
  },
})
