import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/content/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/content/pages',
      params: { namespace: params.namespace },
    })
  },
})
