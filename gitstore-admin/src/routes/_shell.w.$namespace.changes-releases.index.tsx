import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/changes-releases/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/changes-releases/my-changes',
      params: { namespace: params.namespace },
    })
  },
})
