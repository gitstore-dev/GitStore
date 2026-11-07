import { describe, expect, it } from 'vitest'
import {
  createSession,
  sessionReducer,
  projectedResources,
  restoreProblem,
  resetSession,
} from '../../demo/model'
import {
  productAt,
  emptyProduct,
  productProblem,
  nextProductId,
  productList,
  productConnection,
  productSearch,
  staleBuffer,
  deletionBlockers,
  reorderMedia,
} from './model'
import type { Product } from './model'
const seed = () => createSession('atelier-kyoto')
const value = (): Product => ({
  ...emptyProduct(),
  body: '# A thoughtful object\n\n**Handmade** for everyday.',
  spec: { ...emptyProduct().spec, title: 'New cup' },
})
describe('Product demo catalog', () => {
  it('creates deterministically with immutable unique identity and Processing, without publication', () => {
    const session = seed(),
      product = value(),
      id = nextProductId(session, product.spec.title)
    const next = sessionReducer(session, {
      type: 'product-save',
      id,
      value: product,
      base: null,
    })
    expect(productAt(next, id)).toEqual(product)
    expect(next.resources.at(-1)).toMatchObject({
      id,
      readiness: 'reconciling',
      path: `catalog/products/${id}.md`,
    })
    expect(next.events[0]).toMatchObject({
      itemId: id,
      label: 'Product created',
      after: product,
      local: true,
    })
    expect(nextProductId(next, product.spec.title)).toBe('new-cup-2')
    expect(nextProductId(session, 'Namespace')).toBe('namespace-2')
    expect(nextProductId(session, 'New')).toBe('new-2')
    expect(session.catalog[id]).toBeUndefined()
    const changed = {
      ...product,
      spec: { ...product.spec, title: 'A renamed cup' },
    }
    const updated = sessionReducer(next, {
      type: 'product-save',
      id,
      value: changed,
      base: product,
    })
    expect(updated.resources.at(-1)?.path).toBe(`catalog/products/${id}.md`)
    expect(updated.events[0]?.label).toBe('Product updated')
    expect(createSession('studio-berlin').catalog).toEqual({})
  })
  it('validates Title, references and option identity while allowing an empty Markdown body', () => {
    const session = seed(),
      p = value()
    expect(productProblem(p, session)).toBeNull()
    for (const title of ['', '   ', 'a'.repeat(201)])
      expect(
        productProblem({ ...p, spec: { ...p.spec, title } }, session),
      ).toMatch(/Title/)
    expect(productProblem({ ...p, body: '' }, session)).toBeNull()
    expect(
      productProblem(
        { ...p, spec: { ...p.spec, categoryId: 'missing' } },
        session,
      ),
    ).toMatch(/category/)
    expect(
      productProblem(
        { ...p, spec: { ...p.spec, mediaIds: ['homeware'] } },
        session,
      ),
    ).toMatch(/media/)
    const option = { name: 'size', title: 'Size', values: ['M'] }
    expect(
      productProblem(
        { ...p, spec: { ...p.spec, options: [option, option] } },
        session,
      ),
    ).toMatch(/unique names/)
    expect(
      productProblem(
        {
          ...p,
          spec: { ...p.spec, options: [{ ...option, values: ['M', 'M'] }] },
        },
        session,
      ),
    ).toMatch(/unique values/)
    expect(p.spec).not.toHaveProperty('sku')
    expect(p.spec).not.toHaveProperty('description')
  })
  it('detects option identity changes even when the visible Title and values stay the same', () => {
    const session = seed(),
      base = productAt(session, 'silk-tunic')!
    const nextValue = {
      ...base,
      spec: {
        ...base.spec,
        options: base.spec.options.map((option, index) =>
          index === 0 ? { ...option, name: 'fit' } : option,
        ),
      },
    }
    const next = sessionReducer(session, {
      type: 'product-save',
      id: 'silk-tunic',
      base,
      value: nextValue,
    })
    expect(productAt(next, 'silk-tunic')?.spec.options[0]?.name).toBe('fit')
    expect(staleBuffer(next, 'silk-tunic', { base, value: base })).toBe(true)
  })
  it('blocks stale saves after recovery while preserving Product buffers and personal work', () => {
    const session = seed(),
      base = productAt(session, 'stone-basin')!
    const buffer = { base, value: { ...base, body: 'Unfinished edits' } }
    const editing = sessionReducer(session, {
      type: 'product-buffer',
      id: 'stone-basin',
      buffer,
    })
    const recovered = sessionReducer(editing, {
      type: 'restore',
      eventId: 'history-stone-basin-0',
    })
    expect(staleBuffer(recovered, 'stone-basin', buffer)).toBe(true)
    expect(recovered.productBuffers['stone-basin']).toEqual(buffer)
    expect(recovered.work.items.slice(1)).toEqual(session.work.items)
    expect(
      sessionReducer(recovered, {
        type: 'product-save',
        id: 'stone-basin',
        ...buffer,
      }),
    ).toBe(recovered)
    expect(
      sessionReducer(editing, {
        type: 'product-buffer',
        id: 'stone-basin',
        buffer: null,
      }).catalog,
    ).toBe(session.catalog)
  })
  it('blocks dependent variants without cascading; retirement saves authored content separately', () => {
    const session = seed(),
      base = productAt(session, 'silk-tunic')!
    expect(deletionBlockers(session, 'silk-tunic')).toHaveLength(2)
    expect(
      sessionReducer(session, { type: 'product-delete', id: 'silk-tunic' }),
    ).toBe(session)
    const next = sessionReducer(session, {
      type: 'product-save',
      id: 'silk-tunic',
      base,
      value: {
        ...base,
        spec: { ...base.spec, lifecycle: { state: 'RETIRED' } },
      },
    })
    expect(productAt(next, 'silk-tunic')?.spec.lifecycle.state).toBe('RETIRED')
    expect(deletionBlockers(next, 'silk-tunic')).toHaveLength(2)
    expect(next.events[0]?.label).toBe('Product retired')
  })
  it('requires deletion-in-progress, blocks generic resolution and rechecks blockers at completion', () => {
    const session = seed()
    expect(
      sessionReducer(session, {
        type: 'product-complete-delete',
        id: 'stone-basin',
      }),
    ).toBe(session)
    const deleting = sessionReducer(session, {
      type: 'product-delete',
      id: 'stone-basin',
    })
    expect(deleting.deleting).toContain('stone-basin')
    expect(
      projectedResources(deleting).find((r) => r.id === 'stone-basin'),
    ).toMatchObject({ readiness: 'reconciling', deleting: true })
    expect(
      sessionReducer(deleting, { type: 'resolve', id: 'stone-basin' }),
    ).toBe(deleting)
    expect(restoreProblem(deleting, 'history-stone-basin-0')).toMatch(
      /being deleted/,
    )
    const blocked = {
      ...deleting,
      variantOwners: {
        ...deleting.variantOwners,
        'tea-kit-natural': 'stone-basin',
      },
    }
    expect(
      sessionReducer(blocked, {
        type: 'product-complete-delete',
        id: 'stone-basin',
      }),
    ).toBe(blocked)
    const next = sessionReducer(deleting, {
      type: 'product-complete-delete',
      id: 'stone-basin',
    })
    expect(next.catalog['stone-basin']).toBeUndefined()
    expect(next.work).toBe(session.work)
    expect(next.events[0]?.label).toBe('Product deleted')
    expect(restoreProblem(next, 'history-stone-basin-0')).toMatch(
      /no longer available/,
    )
    expect(() => projectedResources(next)).not.toThrow()
    expect(
      projectedResources(next)
        .find((r) => r.id === 'autumn-objects')
        ?.references.some((r) => r.label === 'Unavailable item'),
    ).toBe(true)
    expect(resetSession(next).catalog).toEqual(session.catalog)
  })
  it('navigates opaque cursors in both directions and refuses stale positions', () => {
    const session = seed(),
      search = productSearch({ sort: 'title' })
    const first = productConnection(session, search, { size: 2 })
    expect(first.edges).toHaveLength(2)
    expect(first.pageInfo).toMatchObject({
      hasNextPage: true,
      hasPreviousPage: false,
    })
    const next = productConnection(session, search, {
      after: first.pageInfo.endCursor!,
      size: 2,
    })
    expect(next.pageInfo).toMatchObject({
      hasNextPage: false,
      hasPreviousPage: true,
    })
    expect(
      new Set([...first.edges, ...next.edges].map((edge) => edge.node[0])).size,
    ).toBe(4)
    const previous = productConnection(session, search, {
      before: next.pageInfo.startCursor!,
      size: 2,
    })
    expect(previous.edges).toEqual(first.edges)
    expect(
      productConnection(session, search, { after: 'unavailable-position' })
        .invalidCursor,
    ).toBe(true)
    expect(
      productConnection(createSession('studio-berlin'), search).pageInfo,
    ).toEqual({
      hasNextPage: false,
      hasPreviousPage: false,
      startCursor: null,
      endCursor: null,
    })
  })
  it('filters and sorts one catalog with normalized URL state and reorders media without duplicates', () => {
    const session = seed()
    expect(productList(session, productSearch({}))).toHaveLength(4)
    expect(productList(session, productSearch({ q: ' KANSO ' }))[0]?.[0]).toBe(
      'stone-basin',
    )
    expect(
      productList(session, productSearch({ status: 'attention' })),
    ).toHaveLength(2)
    expect(
      productList(
        session,
        productSearch({ status: 'ready', category: 'homeware' }),
      ),
    ).toHaveLength(2)
    expect(
      productList(session, productSearch({ lifecycle: 'RETIRED' })),
    ).toEqual([])
    expect(
      productSearch({ after: 5, status: 'invented', sort: 'bad' }),
    ).toMatchObject({ status: 'all', sort: 'updated' })
    expect(
      productList(session, productSearch({ sort: 'title' }))[0]?.[1].spec.title,
    ).toBe('Ceramic Everyday Cup')
    expect(reorderMedia(['a', 'b', 'c'], 1, -1)).toEqual(['b', 'a', 'c'])
    expect(reorderMedia(['a', 'b'], 0, -1)).toEqual(['a', 'b'])
  })
})
