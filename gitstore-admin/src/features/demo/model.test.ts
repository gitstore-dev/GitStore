import { describe, expect, it } from 'vitest'
import {
  createSession,
  emptyFilters,
  filterActivity,
  groupActivity,
  projectedResources,
  resetSession,
  restoreProblem,
  sessionReducer,
} from './model'
import {
  recoveryProblem,
  sameSnapshot,
  snapshotChanges,
  snapshotTitle,
} from './snapshots'
import type { ItemSnapshot } from './snapshots'
import { resourceLabel } from '../home/overview/model'
const seed = () => createSession('atelier-kyoto')
const older = (kind: ItemSnapshot['kind']) =>
  seed().events.find(
    (event) =>
      event.after?.kind === kind &&
      snapshotTitle(event.after) !==
        snapshotTitle(seed().catalog[event.itemId]!),
  )!
describe('shared Home demo session', () => {
  it('seeds coherent immutable histories, canonical titles, valid links and exactly 24 events', () => {
    const session = seed()
    expect(session.events).toHaveLength(24)
    expect(createSession('atelier-kyoto')).toEqual(session)
    expect(new Set(session.events.map((event) => event.id)).size).toBe(24)
    expect(
      new Set(session.events.map((event) => event.timestamp.slice(0, 10))),
    ).toEqual(new Set(['2026-10-31', '2026-11-01', '2026-11-02']))
    for (const [id, snapshot] of Object.entries(session.catalog)) {
      const latest = session.events.find(
        (event) => event.itemId === id && event.after,
      )
      expect(latest?.after).toEqual(snapshot)
      if (snapshot.kind === 'Product') {
        expect(snapshot.spec).not.toHaveProperty('sku')
        expect(snapshot.spec).not.toHaveProperty('name')
      }
      if (snapshot.kind !== 'File')
        expect(snapshot.spec.title.trim()).not.toBe('')
    }
    for (const resource of session.resources.filter((item) =>
      ['Product', 'ProductVariant', 'CategoryTaxonomy', 'Collection'].includes(
        item.kind,
      ),
    )) {
      expect(resource).toHaveProperty('spec.title')
      expect(resource).not.toHaveProperty('name')
    }
    expect(createSession('studio-berlin').catalog).toEqual({})
    expect(createSession('studio-berlin').events).toEqual([])
  })
  it('filters by title, actor, canonical type, event category and UTC date without changing history', () => {
    const session = seed()
    const event = session.events[0]!
    const result = filterActivity(session.events, {
      query: event.itemTitle,
      actor: event.actor,
      kind: event.kind,
      category: event.category,
      date: event.timestamp.slice(0, 10),
    })
    expect(result.some((item) => item.id === event.id)).toBe(true)
    expect(
      result.every(
        (item) => item.actor === event.actor && item.kind === event.kind,
      ),
    ).toBe(true)
    expect(
      filterActivity(session.events, {
        ...emptyFilters,
        query: ' no such title ',
      }),
    ).toEqual([])
    expect(
      groupActivity(filterActivity(session.events, emptyFilters)).map(
        (group) => group.date,
      ),
    ).toEqual(['2026-11-02', '2026-11-01', '2026-10-31'])
    expect(
      filterActivity(session.events, emptyFilters).slice(0, 10),
    ).toHaveLength(10)
  })
  for (const kind of [
    'Product',
    'CategoryTaxonomy',
    'Collection',
    'File',
  ] as const) {
    it(`atomically restores a complete ${kind} snapshot and records completed work without readiness changes`, () => {
      const session = seed()
      const event = older(kind)
      expect(restoreProblem(session, event.id)).toBeNull()
      const next = sessionReducer(session, {
        type: 'restore',
        eventId: event.id,
      })
      expect(next.catalog[event.itemId]).toEqual(event.after)
      expect(next.resources).toBe(session.resources)
      expect(next.events.slice(1)).toEqual(session.events)
      expect(next.events[0]).toMatchObject({
        sourceEventId: event.id,
        category: 'recovery',
        local: true,
        actor: 'Elena Rostova',
      })
      expect(next.work.items[0]).toMatchObject({
        entryType: 'recovery',
        resourceKind: kind,
        status: 'completed',
        outcome: 'restored',
        recovery: { after: event.after },
      })
      expect(next.work.items.slice(1)).toEqual(session.work.items)
      expect(session.catalog[event.itemId]).not.toEqual(event.after)
      expect(restoreProblem(next, event.id)).toMatch(/already current/)
      expect(sessionReducer(next, { type: 'restore', eventId: event.id })).toBe(
        next,
      )
      const current = projectedResources(next).find(
        (resource) => resource.id === event.itemId,
      )!
      expect(resourceLabel(current)).toBe(snapshotTitle(event.after!))
    })
  }
  it('compares authored fields and ordered links independently of object key ordering', () => {
    const session = seed()
    const event = session.events.find(
      (event) => event.id === 'history-stone-basin-0',
    )!
    const changes = snapshotChanges(
      session.catalog[event.itemId],
      event.after!,
      session.catalog,
    )
    expect(changes.map((change) => change.label)).toContain('Title')
    expect(changes.map((change) => change.label)).toContain('Product images')
    expect(changes.find((change) => change.label === 'Category')?.before).toBe(
      'Homeware',
    )
    const original = session.catalog[event.itemId]!
    if (original.kind !== 'Product') throw new Error('Product expected')
    expect(
      sameSnapshot(original, { ...original, spec: { ...original.spec } }),
    ).toBe(true)
  })
  it('rejects unavailable versions, wrong-kind or missing links and category cycles without mutation', () => {
    const session = seed()
    const product = session.catalog['stone-basin']!
    if (product.kind !== 'Product') throw new Error('Product expected')
    expect(
      recoveryProblem(
        'stone-basin',
        { ...product, spec: { ...product.spec, categoryId: 'basin-image' } },
        session.catalog,
      ),
    ).toMatch(/missing or has the wrong type/)
    expect(
      recoveryProblem(
        'stone-basin',
        { ...product, spec: { ...product.spec, mediaIds: ['missing'] } },
        session.catalog,
      ),
    ).toMatch(/missing or has the wrong type/)
    const category = session.catalog.homeware!
    if (category.kind !== 'CategoryTaxonomy')
      throw new Error('Category expected')
    expect(
      recoveryProblem(
        'homeware',
        { ...category, spec: { ...category.spec, parentId: 'homeware' } },
        session.catalog,
      ),
    ).toMatch(/cycle/)
    expect(recoveryProblem('missing', product, session.catalog)).toMatch(
      /no longer available/,
    )
    expect(
      sessionReducer(session, { type: 'restore', eventId: 'missing' }),
    ).toBe(session)
    const invalid = createSession('atelier-kyoto', {
      activityScenario: 'recovery-error',
    })
    const bad = invalid.events.find(
      (event) =>
        event.after?.kind === 'Product' &&
        event.after.spec.categoryId === 'missing-category',
    )!
    expect(sessionReducer(invalid, { type: 'restore', eventId: bad.id })).toBe(
      invalid,
    )
  })
  it('preserves drafts and unsaved buffers during recovery and records only explicit saved work actions', () => {
    let session = seed()
    const text = { spec: { title: 'Unsaved title' }, body: '' }
    session = sessionReducer(session, {
      type: 'buffer',
      id: 'basin-draft',
      text,
    })
    expect(session.events).toHaveLength(24)
    const next = sessionReducer(session, {
      type: 'restore',
      eventId: older('Product').id,
    })
    expect(next.buffers['basin-draft']).toEqual(text)
    expect(next.work.items.slice(1)).toEqual(session.work.items)
    const saved = sessionReducer(next, {
      type: 'work',
      action: { type: 'save', id: 'basin-draft', text },
    })
    expect(saved.buffers['basin-draft']).toBeUndefined()
    expect(saved.events[0]?.label).toBe('Draft saved')
    expect(saved.catalog).toBe(next.catalog)
    const cancelled = sessionReducer(saved, {
      type: 'buffer',
      id: 'basin-draft',
      text: null,
    })
    expect(cancelled.events).toBe(saved.events)
    expect(
      sessionReducer(saved, {
        type: 'work',
        action: { type: 'approve', id: saved.work.items[0]!.id },
      }),
    ).toBe(saved)
  })
  it('records resolution and reviews without publishing or modifying authored catalog data', () => {
    const session = seed()
    const resolved = sessionReducer(session, {
      type: 'resolve',
      id: 'stone-basin',
    })
    expect(
      resolved.resources.find((resource) => resource.id === 'stone-basin')
        ?.readiness,
    ).toBe('ready')
    expect(resolved.catalog).toBe(session.catalog)
    expect(resolved.events[0]?.category).toBe('processing')
    expect(resolved.events[0]).not.toHaveProperty('after')
    const approved = sessionReducer(resolved, {
      type: 'work',
      action: { type: 'approve', id: 'basin-review' },
    })
    expect(approved.catalog).toBe(session.catalog)
    expect(approved.events[0]?.label).toBe('Review approved')
    expect(approved.events[0]?.timestamp).toBe('2026-11-02T12:00:02.000Z')
  })
  it('isolates stores and resets a complete session including buffers, work and history', () => {
    const berlin = createSession('studio-berlin')
    const session = sessionReducer(seed(), {
      type: 'buffer',
      id: 'basin-draft',
      text: { spec: { title: 'Unsaved' }, body: '' },
    })
    const restored = sessionReducer(session, {
      type: 'restore',
      eventId: older('File').id,
    })
    const reset = resetSession(restored)
    expect(reset).toEqual({ ...seed(), epoch: 1 })
    expect(berlin).toEqual(createSession('studio-berlin'))
  })
})
