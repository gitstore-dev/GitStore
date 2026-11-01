import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/settings/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/settings/store-details',
      params: { namespace: params.namespace },
    })
  },
})
