import { createSession, sessionReducer, resetSession } from '../../demo/model'
import { resourceLabel } from './model'
import { describe, expect, it } from 'vitest'
import { overviewFixture } from './fixtures'
import {
  healthMatrix,
  overviewSummary,
  queueCounts,
  resourceKinds,
  workQueue,
} from './model'
const fixture = overviewFixture('atelier-kyoto')
describe('Overview demo fixtures and selectors', () => {
  it('uses unique resources, valid references, fixed activity, and all seven kinds', () => {
    const ids = new Set(fixture.resources.map((resource) => resource.id))
    expect(ids.size).toBe(fixture.resources.length)
    expect(new Set(fixture.resources.map((resource) => resource.kind))).toEqual(
      new Set(resourceKinds),
    )
    for (const resource of fixture.resources)
      for (const reference of resource.references)
        if (reference.resourceId)
          expect(ids.has(reference.resourceId)).toBe(true)
    for (const activity of fixture.activity)
      expect(ids.has(activity.resourceId)).toBe(true)
    expect(fixture).toEqual(overviewFixture('atelier-kyoto'))
    expect(overviewFixture('studio-berlin').resources).toHaveLength(0)
  })
  it('derives coherent summaries and work counts from the same records', () => {
    expect(overviewSummary(fixture.resources)).toEqual({
      total: 18,
      attention: 4,
      reconciling: 2,
      ready: 12,
    })
    expect(queueCounts(fixture.resources)).toEqual({
      all: 6,
      failed: 1,
      blocked: 2,
      stale: 1,
      reconciling: 2,
    })
    const matrix = healthMatrix(fixture.resources)
    expect(matrix.reduce((sum, row) => sum + row.total, 0)).toBe(18)
    for (const row of matrix)
      expect(row.total).toBe(row.ready + row.attention + row.reconciling)
  })
  it('filters non-ready resources and sorts by severity then title, or by title', () => {
    expect(
      workQueue(fixture.resources, 'all', 'severity').map(
        (resource) => resource.readiness,
      ),
    ).toEqual([
      'failed',
      'blocked',
      'blocked',
      'stale',
      'reconciling',
      'reconciling',
    ])
    expect(workQueue(fixture.resources, 'blocked', 'severity')).toHaveLength(2)
    const names = workQueue(fixture.resources, 'all', 'title').map((resource) =>
      resourceLabel(resource),
    )
    expect(names).toEqual([...names].sort((a, b) => a.localeCompare(b, 'en')))
  })
  it('resolves locally without mutating fixtures, records activity, and resets exactly', () => {
    const initial = createSession('atelier-kyoto')
    const changed = sessionReducer(initial, {
      type: 'resolve',
      id: 'stone-basin',
    })
    expect(overviewSummary(changed.resources)).toEqual({
      total: 18,
      attention: 3,
      reconciling: 2,
      ready: 13,
    })
    expect(
      workQueue(changed.resources, 'all', 'severity').some(
        (resource) => resource.id === 'stone-basin',
      ),
    ).toBe(false)
    expect(changed.events[0]).toMatchObject({
      itemId: 'stone-basin',
      local: true,
    })
    expect(
      fixture.resources.find((resource) => resource.id === 'stone-basin')!
        .readiness,
    ).toBe('blocked')
    expect(
      sessionReducer(changed, { type: 'resolve', id: 'stone-basin' }),
    ).toBe(changed)
    expect(sessionReducer(changed, { type: 'resolve', id: 'unknown' })).toBe(
      changed,
    )
    expect(resetSession(changed)).toEqual({ ...initial, epoch: 1 })
  })
  it('provides an all-clear state and a distinct filtered-empty state', () => {
    const clear = overviewFixture('atelier-kyoto', 'all-clear')
    expect(queueCounts(clear.resources).all).toBe(0)
    const filtered = overviewFixture('atelier-kyoto', 'filtered-empty')
    expect(workQueue(filtered.resources, 'failed', 'severity')).toHaveLength(0)
    expect(
      workQueue(filtered.resources, 'all', 'severity').length,
    ).toBeGreaterThan(0)
  })
})
