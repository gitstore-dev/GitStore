import { describe, expect, it } from 'vitest'
import { createSession, resetSession, sessionReducer } from '../../demo/model'
import { emptyProduct } from '../../catalog/products/model'
import { setupDefinitions, setupProgress, setupSearch } from './model'

describe('shared store setup checklist', () => {
  it('seeds Kyoto at three of four and Berlin at zero with four unique ordered steps', () => {
    expect(setupDefinitions.map((step) => step.id)).toEqual([
      'store-details',
      'categories',
      'first-product',
      'catalog-checks',
    ])
    const kyoto = setupProgress(createSession('atelier-kyoto'))
    expect(kyoto.completed).toBe(3)
    expect(kyoto.total).toBe(4)
    expect(kyoto.nextStep?.id).toBe('catalog-checks')
    expect(setupProgress(createSession('studio-berlin')).completed).toBe(0)
  })
  it('accepts only known task links without retaining unsupported search values', () => {
    for (const step of setupDefinitions)
      expect(setupSearch({ step: step.id, token: 'ignored' })).toEqual({
        step: step.id,
      })
    for (const step of ['invalid', ['categories'], null, undefined, 7])
      expect(setupSearch({ step })).toEqual({})
  })
  it('reviews and reopens guidance idempotently without changing authored data, histories or buffers', () => {
    const initial = createSession('atelier-kyoto')
    const reviewed = sessionReducer(initial, {
      type: 'setup-review',
      step: 'catalog-checks',
      reviewed: true,
    })
    expect(setupProgress(reviewed).completed).toBe(4)
    expect(setupProgress(reviewed).nextStep).toBeUndefined()
    for (const key of [
      'catalog',
      'resources',
      'events',
      'work',
      'buffers',
      'productBuffers',
      'revision',
    ] as const)
      expect(reviewed[key]).toBe(initial[key])
    expect(
      sessionReducer(reviewed, {
        type: 'setup-review',
        step: 'catalog-checks',
        reviewed: true,
      }),
    ).toBe(reviewed)
    const reopened = sessionReducer(reviewed, {
      type: 'setup-review',
      step: 'categories',
      reviewed: false,
    })
    expect(setupProgress(reopened).completed).toBe(3)
    expect(setupProgress(reopened).nextStep?.id).toBe('categories')
    expect(initial.setupReviews['catalog-checks']).toBe(false)
  })
  it('counts saved retired or processing Products, excludes deleting Products, and reopens after final deletion', () => {
    let session = createSession('studio-berlin')
    const product = {
      ...emptyProduct(),
      spec: {
        ...emptyProduct().spec,
        title: 'First product',
        lifecycle: { state: 'RETIRED' as const },
      },
    }
    session = sessionReducer(session, {
      type: 'product-buffer',
      id: 'new',
      buffer: { value: product, base: null },
    })
    expect(setupProgress(session).completed).toBe(0)
    session = sessionReducer(session, {
      type: 'product-save',
      id: 'first-product',
      value: product,
      base: null,
    })
    expect(setupProgress(session).productCount).toBe(1)
    expect(setupProgress(session).completed).toBe(1)
    expect(
      session.resources.find((item) => item.id === 'first-product')?.readiness,
    ).toBe('reconciling')
    session = sessionReducer(session, {
      type: 'product-delete',
      id: 'first-product',
    })
    expect(session.catalog['first-product']).toBeDefined()
    expect(setupProgress(session).deletingProductCount).toBe(1)
    expect(setupProgress(session).productCount).toBe(0)
    session = sessionReducer(session, {
      type: 'product-complete-delete',
      id: 'first-product',
    })
    expect(session.catalog['first-product']).toBeUndefined()
    expect(
      setupProgress(session).steps.find((step) => step.id === 'first-product')
        ?.complete,
    ).toBe(false)
  })
  it('restores review fixtures on reset without carrying reviews into the other store', () => {
    const kyoto = createSession('atelier-kyoto')
    const berlin = sessionReducer(createSession('studio-berlin'), {
      type: 'setup-review',
      step: 'categories',
      reviewed: true,
    })
    expect(setupProgress(berlin).completed).toBe(1)
    expect(setupProgress(resetSession(berlin)).completed).toBe(0)
    expect(resetSession(berlin).epoch).toBe(1)
    expect(kyoto.setupReviews.categories).toBe(true)
    expect(
      setupProgress(
        resetSession(
          sessionReducer(kyoto, {
            type: 'setup-review',
            step: 'catalog-checks',
            reviewed: true,
          }),
        ),
      ).completed,
    ).toBe(3)
  })
})
