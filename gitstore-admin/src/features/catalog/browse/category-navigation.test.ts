import { describe, expect, it } from 'vitest'
import {
  createSession,
  projectedResources,
  sessionReducer,
} from '../../demo/model'
import { recoveryProblem, snapshotFields } from '../../demo/snapshots'
import { catalogEntries, catalogSearch, filteredEntries } from './model'
import {
  categoryColumns,
  categoryContext,
  categoryMediaId,
} from './category-navigation'

describe('category presentation and navigation', () => {
  it('defaults to Columns and migrates old view URLs without changing other domains', () => {
    expect(catalogSearch({}, 'categories').view).toBe('columns')
    for (const [old, next] of [
      ['spatial', 'columns'],
      ['table', 'tree'],
      ['grid', 'cards'],
    ])
      expect(catalogSearch({ view: old }, 'categories').view).toBe(next)
    expect(catalogSearch({ view: 'grid' }, 'variants').view).toBe('grid')
  })
  it('uses only category-owned file references and handles missing, empty, and invalid media', () => {
    const session = createSession('atelier-kyoto')
    expect(categoryMediaId(session, 'ceramics')).toBe('basin-image')
    expect(categoryMediaId(session, 'lifestyle')).toBeUndefined()
    expect(
      categoryMediaId(
        createSession('atelier-kyoto', { catalogScenario: 'missing-media' }),
        'ceramics',
      ),
    ).toBeUndefined()
    expect(
      categoryMediaId(
        {
          catalog: {
            ...session.catalog,
            'basin-image': session.catalog.homeware!,
          },
        },
        'ceramics',
      ),
    ).toBeUndefined()
  })
  it('includes ancestors for orientation while keeping the actual match set distinct', () => {
    const session = createSession('atelier-kyoto')
    const matches = filteredEntries(
      session,
      'categories',
      catalogSearch({ q: 'Tableware' }, 'categories'),
    )
    const context = categoryContext(session, matches)
    expect([...context.matchIds]).toEqual(['tableware'])
    expect(context.rows.map((row) => row.item.id)).toEqual([
      'homeware',
      'ceramics',
      'tableware',
    ])
    expect(
      categoryColumns(session, context.entries, 'tableware').columns.map(
        (column) => column.parent,
      ),
    ).toEqual([null, 'homeware', 'ceramics'])
  })
  it('constructs deep paths without empty leaf columns and bounds cycles and missing parents', () => {
    const deep = createSession('atelier-kyoto', { catalogScenario: 'deep' })
    const columns = categoryColumns(
      deep,
      catalogEntries(deep, 'categories'),
      'category-level-12',
    )
    expect(columns.path).toHaveLength(12)
    expect(columns.columns).toHaveLength(12)
    for (const scenario of ['cycle', 'missing-parent'] as const) {
      const session = createSession('atelier-kyoto', {
        catalogScenario: scenario,
      })
      const values = catalogEntries(session, 'categories')
      expect(
        categoryColumns(session, values, 'tableware').columns.length,
      ).toBeLessThanOrEqual(values.length + 1)
      expect(
        categoryContext(session, values).rows.some((row) => !!row.problem),
      ).toBe(true)
    }
  })
  it('carries category media through full recovery, preview fields, and related-file inspection', () => {
    const session = createSession('atelier-kyoto')
    const category = session.catalog.homeware!
    if (category.kind !== 'CategoryTaxonomy') throw Error('fixture')
    expect(snapshotFields(category)['Category images']).toEqual(['lookbook'])
    const restored = sessionReducer(session, {
      type: 'restore',
      eventId: 'history-homeware-0',
    })
    expect(
      restored.catalog.homeware?.kind === 'CategoryTaxonomy' &&
        restored.catalog.homeware.spec.media,
    ).toEqual(category.spec.media)
    expect(
      projectedResources(restored)
        .find((item) => item.id === 'homeware')
        ?.references.some((ref) => ref.resourceId === 'lookbook'),
    ).toBe(true)
    expect(
      projectedResources(restored)
        .find((item) => item.id === 'lookbook')
        ?.references.some((ref) => ref.resourceId === 'homeware'),
    ).toBe(true)
    for (const optional of [false, true]) {
      const target = {
        ...category,
        spec: {
          ...category.spec,
          title: 'Revised category',
          media: [{ fileRef: { name: 'absent-file', optional } }],
        },
      }
      expect(recoveryProblem('homeware', target, session.catalog)).toBe(
        optional
          ? null
          : 'A related item is missing or has the wrong type. This version cannot be restored.',
      )
    }
  })
})
