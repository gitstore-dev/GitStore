import type { Readiness, ResourceKind } from './model'

/** Merchant-facing copy; canonical kinds and readiness values stay in the model. */
export const readinessLabels: Record<Readiness, string> = {
  ready: 'Ready',
  blocked: 'Needs review',
  failed: 'Has errors',
  reconciling: 'Processing',
  stale: 'Out of date',
}
export const kindLabels: Record<
  ResourceKind,
  { singular: string; plural: string }
> = {
  Product: { singular: 'Product', plural: 'Products' },
  ProductVariant: { singular: 'Product variant', plural: 'Product variants' },
  CategoryTaxonomy: { singular: 'Category', plural: 'Categories' },
  Collection: { singular: 'Collection', plural: 'Collections' },
  File: { singular: 'Media file', plural: 'Media files' },
  Namespace: { singular: 'Workspace', plural: 'Workspaces' },
  Repository: { singular: 'Data source', plural: 'Data sources' },
}
