import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/inventory/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/inventory/stock-overview',
      params: { namespace: params.namespace },
    })
  },
})
