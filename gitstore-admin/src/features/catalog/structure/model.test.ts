import { describe, expect, it } from 'vitest'
import { createSession, sessionReducer, resetSession } from '../../demo/model'
import {
  structureSearch,
  structureEntries,
  filterStructure,
  structureConnection,
  structureRoute,
  structureEntryForPath,
  structureReturnSearch,
  optionIdentity,
  optionSelections,
} from './model'
import { productJourney } from '../products/productNavigation'
describe('Catalog structure browsers', () => {
  it('separates saved Product options from isolated definition/type examples without adding resources', () => {
    const session = createSession('atelier-kyoto'),
      before = JSON.stringify(session)
    expect(
      structureEntries(session, 'options')
        .map((e) => e.title)
        .sort(),
    ).toEqual(['Color', 'Size'])
    expect(structureEntries(session, 'definitions')).toHaveLength(3)
    expect(structureEntries(session, 'types')).toHaveLength(2)
    for (const tab of ['options', 'definitions', 'types'] as const)
      expect(structureEntries(createSession('studio-berlin'), tab)).toEqual([])
    expect(JSON.stringify(session)).toBe(before)
    expect(
      structureEntries({ ...session, catalog: {} }, 'definitions'),
    ).toHaveLength(3)
  })
  it('uses Product-scoped option identity, including escaped names, and never merges same-name options', () => {
    const session = createSession('atelier-kyoto'),
      product = session.catalog['silk-tunic']!
    if (product.kind !== 'Product') throw Error('fixture')
    const next = {
      ...session,
      catalog: { ...session.catalog, 'extra-product': product },
    }
    const options = structureEntries(next, 'options')
    expect(options.filter((e) => e.title === 'Color')).toHaveLength(2)
    expect(new Set(options.map((e) => e.id)).size).toBe(4)
    const name = 'finish / surface %'
    const route = structureRoute(
      `/w/atelier-kyoto/catalog/attributes-options/products/silk-tunic/options/${encodeURIComponent(name)}`,
    )
    expect(route?.id).toBe(optionIdentity('silk-tunic', name))
    expect(
      structureRoute(
        '/w/atelier-kyoto/catalog/attributes-options/products/a/options/%FF',
      ),
    ).toBeUndefined()
  })
  it('reacts to Product edits and recovery while leaving reusable definitions unchanged', () => {
    const session = createSession('atelier-kyoto'),
      product = session.catalog['silk-tunic']!
    if (product.kind !== 'Product') throw Error('fixture')
    const original = structureEntries(session, 'definitions')
    const next = sessionReducer(session, {
      type: 'product-save',
      id: 'silk-tunic',
      base: product,
      value: {
        ...product,
        spec: {
          ...product.spec,
          title: 'Revised Tunic',
          options: [{ name: 'finish', title: 'Finish', values: ['Matte'] }],
        },
      },
    })
    expect(structureEntries(next, 'options').map((e) => e.title)).toEqual([
      'Finish',
    ])
    expect(
      structureEntryForPath(
        next,
        '/w/atelier-kyoto/catalog/attributes-options/products/silk-tunic/options/color',
      ),
    ).toBeUndefined()
    expect(structureEntries(next, 'definitions')).toEqual(original)
    expect(
      structureEntries(resetSession(next, {}), 'options')
        .map((e) => e.title)
        .sort(),
    ).toEqual(['Color', 'Size'])
    const old = session.events.find(
      (e) =>
        e.itemId === 'silk-tunic' &&
        e.after?.kind === 'Product' &&
        e.after.spec.options.length === 2,
    )!
    const restored = sessionReducer(next, {
      type: 'restore',
      eventId: old.id,
    })
    expect(
      structureEntries(restored, 'options')
        .map((e) => e.title)
        .sort(),
    ).toEqual(['Color', 'Size'])
  })
  it('matches Variant selections only through the owning Product and flags absent/unoffered choices', () => {
    const session = createSession('atelier-kyoto')
    const color = structureEntries(session, 'options').find(
      (e) => e.title === 'Color',
    )!
    if (color.source !== 'option') throw Error('fixture')
    expect(
      optionSelections(session, color)
        .map((e) => e.value)
        .sort(),
    ).toEqual(['Black', 'Natural'])
    expect(optionSelections(session, color).every((e) => e.offered)).toBe(true)
    expect(
      optionSelections(session, { ...color, values: ['Natural'] }).filter(
        (e) => !e.offered,
      ),
    ).toHaveLength(1)
    expect(
      optionSelections(session, { ...color, optionName: 'missing' }).every(
        (e) => e.value === undefined && !e.offered,
      ),
    ).toBe(true)
  })
  it('validates URL state and restores definition tabs for direct detail links', () => {
    expect(structureSearch({}, 'attributes-options')).toEqual({
      q: '',
      related: 'all',
      sort: 'updated',
      view: 'table',
      tab: 'options',
    })
    expect(
      structureSearch(
        { tab: 'invalid', view: 'spatial', sort: 'priority', related: 7, q: 9 },
        'product-types',
      ),
    ).toEqual({ ...structureSearch({}, 'attributes-options'), tab: 'types' })
    expect(
      structureReturnSearch(
        {},
        'attributes-options',
        '/w/atelier-kyoto/catalog/attributes-options/definitions/color',
      ).tab,
    ).toBe('definitions')
  })
  it('filters by owning Product or example relationship, with deterministic ordering and no inferred assignments', () => {
    const session = createSession('atelier-kyoto')
    expect(
      filterStructure(
        structureEntries(session, 'options'),
        structureSearch({ q: 'Tunic' }, 'attributes-options'),
        session,
      ),
    ).toHaveLength(2)
    expect(
      filterStructure(
        structureEntries(session, 'options'),
        structureSearch({ related: 'stone-basin' }, 'attributes-options'),
        session,
      ),
    ).toEqual([])
    expect(
      filterStructure(
        structureEntries(session, 'definitions'),
        structureSearch(
          { tab: 'definitions', related: 'apparel' },
          'attributes-options',
        ),
        session,
      ).map((e) => e.title),
    ).toEqual(['Color', 'Size'])
    expect(
      filterStructure(
        structureEntries(session, 'types'),
        structureSearch({ related: 'material' }, 'product-types'),
        session,
      ).map((e) => e.title),
    ).toEqual(['Homeware'])
    expect(
      filterStructure(
        structureEntries(session, 'definitions'),
        structureSearch(
          { tab: 'definitions', sort: 'title' },
          'attributes-options',
        ),
        session,
      ).map((e) => e.title),
    ).toEqual(['Color', 'Material', 'Size'])
    expect(
      filterStructure(
        structureEntries(session, 'types'),
        structureSearch({ q: 'missing' }, 'product-types'),
        session,
      ),
    ).toEqual([])
  })
  it('bounds cursor batches and treats stale cursors as unavailable, not page one', () => {
    const entries = structureEntries(
      createSession('atelier-kyoto'),
      'types',
      'many',
    )
    const a = structureConnection(entries),
      b = structureConnection(entries, a.pageInfo.endCursor),
      c = structureConnection(entries, b.pageInfo.endCursor)
    expect(a.edges).toHaveLength(6)
    expect(b.edges).toHaveLength(6)
    expect(c.edges).toHaveLength(3)
    expect(c.pageInfo.hasNextPage).toBe(false)
    expect(
      new Set([...a.edges, ...b.edges, ...c.edges].map((e) => e.node.id)).size,
    ).toBe(15)
    expect(structureConnection(entries, 'stale').edges).toEqual([])
  })
  it('pairs complex option/detail journeys and concept paths without crossing domains', () => {
    const root = '/w/atelier-kyoto/catalog/attributes-options',
      detail = `${root}/products/silk-tunic/options/color`
    expect(productJourney(root, detail)).toMatchObject({
      id: optionIdentity('silk-tunic', 'color'),
      direction: 'open',
    })
    expect(productJourney(detail, root)).toMatchObject({ direction: 'return' })
    expect(productJourney(root, `${root}/definitions/color`)).toMatchObject({
      id: 'definition:color',
    })
    expect(
      productJourney(root, '/w/atelier-kyoto/catalog/product-types/apparel'),
    ).toBeUndefined()
    expect(
      structureEntryForPath(
        createSession('atelier-kyoto'),
        '/w/atelier-kyoto/catalog/product-types/apparel',
      )?.title,
    ).toBe('Apparel')
  })
})
