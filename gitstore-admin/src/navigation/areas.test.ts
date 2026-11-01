import { describe, expect, it } from 'vitest'
import { areaFor, areaEntry, workspaceAreas } from './areas'
import { destinations, groups } from './registry'

describe('contextual navigation', () => {
  it('assigns every workspace group to one area while keeping Settings and platform separate', () => {
    const assigned = workspaceAreas.flatMap((area) => [...area.groups])
    expect(new Set(assigned).size).toBe(assigned.length)
    expect([...assigned].sort()).toEqual(
      groups
        .filter(
          (group) => group.scope === 'namespace' && group.id !== 'settings',
        )
        .map((group) => group.id)
        .sort(),
    )
    for (const destination of destinations) {
      expect(Boolean(areaFor(destination))).toBe(
        destination.scope === 'namespace' && destination.groupId !== 'settings',
      )
    }
    expect(areaFor(undefined)).toBeUndefined()
  })
  it('uses existing scoped routes as area entry points', () => {
    expect(workspaceAreas.map((area) => area.label)).toEqual([
      'Home',
      'Commerce',
      'Content',
      'Operations',
      'Insights',
    ])
    for (const area of workspaceAreas) {
      expect(destinations).toContain(areaEntry(area))
      expect(areaEntry(area).scope).toBe('namespace')
    }
  })
})
