import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import type { NamespaceId } from '../../../data/demo'
import { structureSearch } from './model'
import type { StructureEntry, StructureSearch } from './model'
export function StructureLink({
  entry,
  namespace,
  search,
  children,
  ...props
}: {
  entry: StructureEntry
  namespace: NamespaceId
  search?: StructureSearch
  children: ReactNode
  className?: string
  'data-resource-id'?: string
  'data-product-surface'?: string
  'data-catalog-card'?: string
}) {
  if (entry.source === 'option')
    return (
      <Link
        {...props}
        to="/w/$namespace/catalog/attributes-options/products/$productId/options/$optionName"
        params={{
          namespace,
          productId: entry.productId,
          optionName: entry.optionName,
        }}
        search={search}
      >
        {children}
      </Link>
    )
  if (entry.source === 'definition')
    return (
      <Link
        {...props}
        to="/w/$namespace/catalog/attributes-options/definitions/$definitionId"
        params={{ namespace, definitionId: entry.exampleId }}
        search={
          search
            ? { ...search, tab: 'definitions' }
            : structureSearch({ tab: 'definitions' }, 'attributes-options')
        }
      >
        {children}
      </Link>
    )
  return (
    <Link
      {...props}
      to="/w/$namespace/catalog/product-types/$typeId"
      params={{ namespace, typeId: entry.exampleId }}
      search={search ?? structureSearch({}, 'product-types')}
    >
      {children}
    </Link>
  )
}
