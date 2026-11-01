import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/insights/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/insights/overview',
      params: { namespace: params.namespace },
    })
  },
})
