import { describe, expect, it } from 'vitest'
import {
  createSession,
  projectedResources,
  sessionReducer,
  resetSession,
} from '../../demo/model'
import {
  catalogEntries,
  catalogSearch,
  filteredEntries,
  catalogConnection,
  matchesLabels,
  matchingProducts,
  categoryHierarchy,
} from './model'
import { productJourney } from '../products/productNavigation'
describe('read-only catalog browsers', () => {
  it('uses shared fixture titles, variant owners, a four-category hierarchy and three rule-based collections', () => {
    const session = createSession('atelier-kyoto')
    expect(catalogEntries(session, 'variants')).toHaveLength(3)
    expect(catalogEntries(session, 'categories')).toHaveLength(4)
    expect(catalogEntries(session, 'collections')).toHaveLength(3)
    for (const [id, variant] of Object.entries(session.variants)) {
      expect(session.catalog[variant.spec.productId]?.kind).toBe('Product')
      expect(session.variantOwners[id]).toBe(variant.spec.productId)
      expect(variant.spec.sku).not.toBe('')
    }
    expect(
      categoryHierarchy(session).map((row) => [row.item.id, row.depth]),
    ).toEqual([
      ['homeware', 0],
      ['ceramics', 1],
      ['tableware', 2],
      ['lifestyle', 0],
    ])
    expect(session.resources).toHaveLength(18)
    expect(session.events).toHaveLength(32)
    expect(catalogEntries(createSession('studio-berlin'), 'variants')).toEqual(
      [],
    )
  })
  it('matches canonical AND label rules and empty selectors without treating merchant tags as labels', () => {
    expect(matchesLabels({}, undefined)).toBe(false)
    expect(matchesLabels({ a: '1' }, {})).toBe(false)
    const selector = {
      matchLabels: { season: 'autumn' },
      matchExpressions: [
        { key: 'material', operator: 'In' as const, values: ['ceramic'] },
        { key: 'draft', operator: 'DoesNotExist' as const },
        { key: 'season', operator: 'Exists' as const },
        { key: 'tier', operator: 'NotIn' as const, values: ['excluded'] },
      ],
    }
    expect(
      matchesLabels({ season: 'autumn', material: 'ceramic' }, selector),
    ).toBe(true)
    expect(
      matchesLabels({ season: 'winter', material: 'ceramic' }, selector),
    ).toBe(false)
    expect(
      matchesLabels(
        { season: 'autumn', material: 'ceramic', draft: '' },
        selector,
      ),
    ).toBe(false)
    expect(
      matchesLabels({ season: 'autumn', material: 'silk' }, selector),
    ).toBe(false)
    expect(
      matchesLabels(
        {},
        {
          matchExpressions: [
            { key: 'missing', operator: 'NotIn', values: ['x'] },
          ],
        },
      ),
    ).toBe(true)
    const session = createSession('atelier-kyoto')
    expect(
      matchingProducts(session, { matchLabels: { assortment: 'autumn' } }),
    ).toEqual(['stone-basin', 'tea-ritual'])
    expect(
      matchingProducts(
        { ...session, deleting: ['stone-basin'] },
        { matchLabels: { assortment: 'autumn' } },
      ),
    ).toEqual(['tea-ritual'])
    expect(matchingProducts(session, undefined)).toEqual([])
  })
  it('validates search, filters Title/SKU/status/parent, and sorts deterministically', () => {
    const s = createSession('atelier-kyoto')
    expect(
      catalogSearch(
        { view: 'spatial', status: 'bad', sort: 'bad' },
        'variants',
      ),
    ).toMatchObject({ view: 'table', status: 'all', sort: 'updated' })
    expect(
      filteredEntries(
        s,
        'variants',
        catalogSearch({ q: 'TUNIC-BLK' }, 'variants'),
      ).map((v) => v.id),
    ).toEqual(['tunic-black-m'])
    expect(
      filteredEntries(
        s,
        'variants',
        catalogSearch({ related: 'tea-ritual' }, 'variants'),
      ).map((v) => v.id),
    ).toEqual(['tea-kit-natural'])
    expect(
      filteredEntries(
        s,
        'categories',
        catalogSearch({ related: 'homeware' }, 'categories'),
      ).map((v) => v.id),
    ).toEqual(['ceramics'])
    expect(
      filteredEntries(
        s,
        'categories',
        catalogSearch({ related: 'root' }, 'categories'),
      ),
    ).toHaveLength(2)
    expect(
      filteredEntries(
        s,
        'collections',
        catalogSearch({ related: 'empty' }, 'collections'),
      ).map((v) => v.id),
    ).toEqual(['seasonal-selection'])
    expect(
      filteredEntries(
        s,
        'variants',
        catalogSearch({ status: 'attention' }, 'variants'),
      ).map((v) => v.id),
    ).toEqual(['tunic-natural-m'])
    expect(
      filteredEntries(
        s,
        'variants',
        catalogSearch({ q: 'no matches' }, 'variants'),
      ),
    ).toEqual([])
  })
  it('uses bounded cursor batches without a required total count', () => {
    const values = catalogEntries(createSession('atelier-kyoto'), 'categories')
    const entries = Array.from({ length: 14 }, (_, index) => ({
      ...values[0]!,
      id: String(index),
    }))
    const first = catalogConnection(entries),
      second = catalogConnection(entries, first.pageInfo.endCursor),
      last = catalogConnection(entries, second.pageInfo.endCursor)
    expect(first.edges).toHaveLength(6)
    expect(second.edges).toHaveLength(6)
    expect(last.edges).toHaveLength(2)
    expect(last.pageInfo.hasNextPage).toBe(false)
    expect(
      new Set(
        [...first.edges, ...second.edges, ...last.edges].map(
          (edge) => edge.node.id,
        ),
      ).size,
    ).toBe(14)
  })
  it('keeps missing parents and cycles inspectable, with no recursive traversal failure', () => {
    const missing = createSession('atelier-kyoto', {
      catalogScenario: 'missing-parent',
    })
    expect(
      categoryHierarchy(missing).find((r) => r.item.id === 'ceramics')?.problem,
    ).toContain('unavailable')
    const cycle = createSession('atelier-kyoto', { catalogScenario: 'cycle' })
    expect(categoryHierarchy(cycle)).toHaveLength(4)
    expect(
      categoryHierarchy(cycle).find((r) => r.item.id === 'homeware')?.problem,
    ).toContain('cycle')
  })
  it('keeps recorded membership separate from current preview, preserves rules on recovery and resets both stores independently', () => {
    let session = createSession('atelier-kyoto')
    const original = session
    const product = session.catalog['stone-basin']!
    if (product.kind !== 'Product') throw Error('fixture')
    session = sessionReducer(session, {
      type: 'product-save',
      id: 'stone-basin',
      base: product,
      value: { ...product, metadata: { labels: { assortment: 'everyday' } } },
    })
    expect(session.collectionMembers['autumn-objects']).toEqual(
      original.collectionMembers['autumn-objects'],
    )
    const collection = session.catalog['autumn-objects']!
    if (collection.kind !== 'Collection') throw Error('fixture')
    expect(matchingProducts(session, collection.spec.selector)).toEqual([
      'tea-ritual',
    ])
    session = sessionReducer(session, { type: 'resolve', id: 'autumn-objects' })
    expect(session.collectionMembers['autumn-objects']).toEqual(['tea-ritual'])
    expect(
      projectedResources(session)
        .find((r) => r.id === 'autumn-objects')
        ?.references.map((r) => r.resourceId),
    ).toEqual(['tea-ritual'])
    expect(resetSession(session).collectionMembers).toEqual(
      original.collectionMembers,
    )
    expect(createSession('studio-berlin').collectionMembers).toEqual({})
    expect(
      original.catalog['autumn-objects']?.kind === 'Collection' &&
        original.catalog['autumn-objects'].spec,
    ).not.toHaveProperty('productIds')
  })
  it('pairs transitions only within the same domain and namespace', () => {
    for (const domain of ['variants', 'categories', 'collections'])
      expect(
        productJourney(
          `/w/atelier-kyoto/catalog/${domain}`,
          `/w/atelier-kyoto/catalog/${domain}/item`,
        ),
      ).toMatchObject({ id: 'item', direction: 'open' })
    expect(
      productJourney(
        '/w/atelier-kyoto/catalog/variants',
        '/w/atelier-kyoto/catalog/products/item',
      ),
    ).toBeUndefined()
  })
  it('sorts recovered category and collection records by their actual latest demo update', () => {
    for (const [domain, id] of [
      ['categories', 'homeware'],
      ['collections', 'autumn-objects'],
    ] as const) {
      const session = createSession('atelier-kyoto')
      const changed = sessionReducer(session, {
        type: 'restore',
        eventId: `history-${id}-0`,
      })
      expect(
        filteredEntries(changed, domain, catalogSearch({}, domain))[0]?.id,
      ).toBe(id)
      expect(changed.productUpdated[id]).not.toBe(session.productUpdated[id])
    }
  })
})
