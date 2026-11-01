import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/channels-markets/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/channels-markets/sales-channels',
      params: { namespace: params.namespace },
    })
  },
})
