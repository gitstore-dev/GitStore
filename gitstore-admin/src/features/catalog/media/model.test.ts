import { describe, expect, it } from 'vitest'
import { createSession, sessionReducer, resetSession } from '../../demo/model'
import {
  mediaEntries,
  mediaSearch,
  filterMedia,
  mediaConnection,
  mediaUsage,
} from './model'
describe('Media browser selectors', () => {
  it('uses the two existing File records without changing catalog or history', () => {
    const session = createSession('atelier-kyoto'),
      before = JSON.stringify(session)
    const entries = mediaEntries(session)
    expect(entries.map((e) => e.id).sort()).toEqual(['basin-image', 'lookbook'])
    expect(mediaEntries(createSession('studio-berlin'))).toEqual([])
    expect(entries.every((e) => e.path.endsWith('.md'))).toBe(true)
    expect(JSON.stringify(session)).toBe(before)
  })
  it('validates URL choices and defaults to Cards with no filters', () => {
    expect(mediaSearch({})).toEqual({
      q: '',
      status: 'all',
      usage: 'all',
      sort: 'updated',
      view: 'grid',
    })
    expect(
      mediaSearch({
        q: 42,
        status: 'published',
        usage: 'deleted',
        sort: 'priority',
        view: 'spatial',
      }),
    ).toEqual(mediaSearch({}))
    expect(
      mediaSearch({
        q: 'basin',
        status: 'blocked',
        usage: 'used',
        sort: 'name',
        view: 'table',
      }).view,
    ).toBe('table')
  })
  it('filters file names, attention states and direct usage with deterministic sorting', () => {
    const entries = mediaEntries(createSession('atelier-kyoto'), 'many')
    expect(
      filterMedia(entries, mediaSearch({ q: 'BASIN' })).map((e) => e.id),
    ).toEqual(['basin-image'])
    expect(
      filterMedia(
        mediaEntries(createSession('atelier-kyoto')),
        mediaSearch({ status: 'reconciling' }),
      ).map((e) => e.id),
    ).toEqual(['lookbook'])
    expect(
      filterMedia(entries, mediaSearch({ usage: 'unused' })).every(
        (e) => e.usage.length === 0,
      ),
    ).toBe(true)
    expect(
      filterMedia(
        entries,
        mediaSearch({ usage: 'used', status: 'attention' }),
      ).map((e) => e.id),
    ).toEqual(['basin-image'])
    const sorted = filterMedia(entries, mediaSearch({ sort: 'name' }))
    expect(sorted.map((e) => e.name)).toEqual(
      sorted.map((e) => e.name).toSorted((a, b) => a.localeCompare(b)),
    )
    expect(filterMedia(entries, mediaSearch({ q: 'no such photo' }))).toEqual(
      [],
    )
  })
  it('appends bounded cursor batches and rejects stale cursors without repeating page one', () => {
    const entries = mediaEntries(createSession('atelier-kyoto'), 'many')
    const first = mediaConnection(entries),
      next = mediaConnection(entries, first.pageInfo.endCursor),
      last = mediaConnection(entries, next.pageInfo.endCursor)
    expect(first.edges).toHaveLength(6)
    expect(next.edges).toHaveLength(6)
    expect(last.edges).toHaveLength(3)
    expect(last.pageInfo.hasNextPage).toBe(false)
    expect(
      new Set(
        [...first.edges, ...next.edges, ...last.edges].map((e) => e.node.id),
      ).size,
    ).toBe(15)
    expect(mediaConnection(entries, 'missing').edges).toEqual([])
  })
  it('derives only explicit Product, Category and Variant references, including retained deleting owners', () => {
    const session = createSession('atelier-kyoto')
    expect(mediaUsage(session, 'basin-image').map((e) => e.kind)).toContain(
      'CategoryTaxonomy',
    )
    const variant =
      session.variants['tunic-natural-m'] ?? Object.values(session.variants)[0]!
    const next = {
      ...session,
      variants: {
        ...session.variants,
        explicit: {
          ...variant,
          spec: { ...variant.spec, mediaIds: ['basin-image'] },
        },
      },
      deleting: ['stone-basin'],
    }
    const used = mediaUsage(next, 'basin-image')
    expect(
      used.some((e) => e.id === 'explicit' && e.kind === 'ProductVariant'),
    ).toBe(true)
    expect(used.some((e) => e.id === 'stone-basin')).toBe(true)
    expect(used.some((e) => String(e.kind) === 'Collection')).toBe(false)
    expect(
      mediaUsage(session, 'basin-image').some(
        (e) => e.kind === 'ProductVariant',
      ),
    ).toBe(false)
    expect(mediaUsage(session, 'missing-file')).toEqual([])
  })
  it('reflects shared recovery names and reset without altering File recovery snapshots', () => {
    const session = createSession('atelier-kyoto')
    const event = session.events.find(
      (e) =>
        e.kind === 'File' &&
        e.after?.kind === 'File' &&
        e.after.name.startsWith('Original'),
    )!
    expect(event).toBeDefined()
    const restored = sessionReducer(session, {
      type: 'restore',
      eventId: event.id,
    })
    expect(
      mediaEntries(restored).find((e) => e.id === event.itemId)?.name,
    ).toBe(event.after?.kind === 'File' ? event.after.name : '')
    expect(mediaEntries(resetSession(restored, {})).map((e) => e.name)).toEqual(
      mediaEntries(session).map((e) => e.name),
    )
  })
  it('keeps declared processing and available outputs independent of readiness and previews', () => {
    const entries = mediaEntries(createSession('atelier-kyoto'))
    const basin = entries.find((e) => e.id === 'basin-image')!,
      lookbook = entries.find((e) => e.id === 'lookbook')!
    expect(basin.details.requests).toHaveLength(2)
    expect(basin.details.outputs).toHaveLength(0)
    expect(lookbook.status).toBe('reconciling')
    expect(lookbook.details.outputs).toHaveLength(1)
    expect(
      mediaEntries(createSession('atelier-kyoto'), 'missing-preview').find(
        (e) => e.id === 'basin-image',
      )?.details.previewId,
    ).toBeUndefined()
    expect(
      mediaEntries(createSession('atelier-kyoto'), 'document').find(
        (e) => e.id === 'basin-image',
      )?.details.contentType,
    ).toBe('application/pdf')
  })
})
