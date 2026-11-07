import type { WorkItem, ProductWorkItem } from './model'
import { describe, expect, it } from 'vitest'
import { workFixture } from './fixtures'
import {
  changedFields,
  initialWork,
  sameText,
  validText,
  workCounts,
  workList,
  workReducer,
  workStatus,
} from './model'
function product(item: WorkItem | undefined): ProductWorkItem {
  if (!item || item.entryType !== 'product-text')
    throw new Error('Expected product work')
  return item
}
const fixture = workFixture('atelier-kyoto')
describe('personal demo work', () => {
  it('has deterministic unique product changes with consistent ownership, paths and counts', () => {
    expect(workFixture('atelier-kyoto')).toEqual(fixture)
    expect(workCounts(fixture)).toEqual({
      drafts: 2,
      submitted: 2,
      assigned: 2,
      completed: 1,
    })
    expect(new Set(fixture.map((item) => item.id)).size).toBe(7)
    for (const item of fixture) {
      expect(item.path).toMatch(/\.md$/)
      expect(changedFields(item)).toEqual(['body'])
      expect(Number.isFinite(Date.parse(item.updatedAt))).toBe(true)
      if (item.status === 'assigned')
        expect(item.reviewer).toBe('Elena Rostova')
      else expect(item.author).toBe('Elena Rostova')
    }
    expect(workFixture('studio-berlin')).toEqual([])
    expect(workFixture('atelier-kyoto', 'empty')).toEqual([])
  })
  it('filters within a tab and sorts deterministically without changing counts or fixtures', () => {
    expect(
      workList(fixture, 'drafts', ' SILK ', 'title').map((item) => item.id),
    ).toEqual(['tunic-draft'])
    expect(workList(fixture, 'drafts', '', 'updated')[0]!.id).toBe(
      'basin-draft',
    )
    expect(
      workList(fixture, 'submitted', '', 'title').map((item) => item.id),
    ).toEqual(['cup-submitted', 'kit-submitted'])
    expect(workList(fixture, 'assigned', 'not found', 'updated')).toEqual([])
    expect(workCounts(fixture).drafts).toBe(2)
  })
  it('validates titles while allowing empty descriptions and preserves the original comparison', () => {
    const state = initialWork(fixture)
    expect(validText({ spec: { title: '  ' }, body: '' })).toBe(false)
    expect(validText({ spec: { title: 'Cup' }, body: '' })).toBe(true)
    expect(
      workReducer(state, {
        type: 'save',
        id: 'basin-draft',
        text: { spec: { title: ' ' }, body: '' },
      }),
    ).toBe(state)
    const next = workReducer(state, {
      type: 'save',
      id: 'basin-draft',
      text: { spec: { title: '  Stone Basin  ' }, body: '' },
    })
    expect(product(next.items[0]).proposed).toEqual({
      spec: { title: 'Stone Basin' },
      body: '',
    })
    expect(product(next.items[0]).base).toEqual(fixture[0]!.base)
    expect(changedFields(next.items[0]!)).toEqual(['title', 'body'])
    expect(sameText(fixture[0]!.base, fixture[0]!.base)).toBe(true)
    expect(sameText(fixture[0]!.base, fixture[0]!.proposed)).toBe(false)
    expect(fixture[0]!.proposed.spec.title).toBe('Kanso Stone Basin')
  })
  it('submits only a saved changed draft and removes it from Drafts', () => {
    const original = initialWork(fixture)
    const unchanged = workReducer(original, {
      type: 'save',
      id: 'basin-draft',
      text: fixture[0]!.base,
    })
    expect(workReducer(unchanged, { type: 'submit', id: 'basin-draft' })).toBe(
      unchanged,
    )
    const next = workReducer(original, { type: 'submit', id: 'basin-draft' })
    expect(workCounts(next.items)).toEqual({
      drafts: 1,
      submitted: 3,
      assigned: 2,
      completed: 1,
    })
    expect(product(next.items[0]).proposed).toEqual(fixture[0]!.proposed)
    expect(workReducer(next, { type: 'submit', id: 'basin-draft' })).toBe(next)
  })
  it('discards only drafts and keeps unrelated items intact', () => {
    const state = initialWork(fixture)
    const next = workReducer(state, { type: 'discard', id: 'basin-draft' })
    expect(next.items).toEqual(fixture.slice(1))
    expect(workReducer(state, { type: 'discard', id: 'cup-submitted' })).toBe(
      state,
    )
  })
  it('records review outcomes and requires a meaningful revision note', () => {
    const state = initialWork(fixture)
    expect(
      workReducer(state, {
        type: 'request-changes',
        id: 'cup-review',
        note: ' ',
      }),
    ).toBe(state)
    const approved = workReducer(state, { type: 'approve', id: 'basin-review' })
    expect(
      workStatus(approved.items.find((item) => item.id === 'basin-review')!),
    ).toBe('Approved')
    const next = workReducer(approved, {
      type: 'request-changes',
      id: 'cup-review',
      note: '  Add care instructions.  ',
    })
    const review = next.items.find((item) => item.id === 'cup-review')!
    expect(review.reviewNote).toBe('Add care instructions.')
    expect(workStatus(review)).toBe('Changes requested')
    expect(workCounts(next.items)).toEqual({
      drafts: 2,
      submitted: 2,
      assigned: 0,
      completed: 3,
    })
    expect(workReducer(next, { type: 'approve', id: 'cup-review' })).toBe(next)
  })
  it('prevents mutations in submitted or completed items and ignores missing identities', () => {
    const state = initialWork(fixture)
    expect(
      workReducer(state, {
        type: 'save',
        id: 'cup-submitted',
        text: { spec: { title: 'Changed' }, body: '' },
      }),
    ).toBe(state)
    expect(workReducer(state, { type: 'approve', id: 'tunic-completed' })).toBe(
      state,
    )
    expect(workReducer(state, { type: 'discard', id: 'missing' })).toBe(state)
  })
  it('resets all local changes to their original deterministic examples', () => {
    const state = initialWork(fixture)
    const changed = workReducer(state, { type: 'discard', id: 'basin-draft' })
    expect(workReducer(changed, { type: 'reset', items: fixture })).toEqual(
      state,
    )
  })
})
