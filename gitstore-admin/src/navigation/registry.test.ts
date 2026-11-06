import { describe, expect, it } from 'vitest'
import {
  destinations,
  groups,
  destinationAtPath,
  destinationHref,
  searchDestinations,
} from './registry'
import { routeTree } from '../routeTree.gen'
function collectRoutes(route: unknown): string[] {
  const r = route as {
    options?: { path?: string }
    fullPath?: string
    children?: unknown[]
  }
  return [
    ...(r.fullPath ? [r.fullPath] : []),
    ...(r.children ?? []).flatMap(collectRoutes),
  ]
}
describe('PRD navigation contract', () => {
  it('keeps all 13 groups and 79 unique, scoped destinations', () => {
    expect(groups).toHaveLength(13)
    expect(destinations).toHaveLength(79)
    expect(new Set(destinations.map((d) => d.id)).size).toBe(79)
    expect(new Set(destinations.map((d) => d.to)).size).toBe(79)
    expect(
      destinations.filter((d) => d.implementation === 'mock').map((d) => d.id),
    ).toEqual(['home.overview', 'home.my-work'])
    expect(
      destinations.filter((d) => d.implementation === 'placeholder'),
    ).toHaveLength(77)
    for (const d of destinations) {
      expect(
        d.to.startsWith(
          d.scope === 'platform' ? '/platform/' : '/w/$namespace/',
        ),
      ).toBe(true)
      expect(d.scope === 'platform').toBe(d.groupId === 'control-plane')
    }
  })
  it('round-trips every destination through namespace URLs and resolves exact active items', () => {
    for (const destination of destinations) {
      expect(
        destinationAtPath(destinationHref(destination, 'studio-berlin'))?.id,
      ).toBe(destination.id)
    }
    expect(destinationAtPath('/unknown')).toBeUndefined()
  })
  it('maps navigation to actual generated file routes', async () => {
    const { createAppRouter } = await import('../app/router')
    const router = createAppRouter({
      initialPath: '/w/atelier-kyoto/home/overview',
    })
    await router.load()
    const routes = collectRoutes(routeTree)
    for (const destination of destinations)
      expect(routes).toContain(destination.to)
  })
  it('searches group and page names together without manufacturing resource results', () => {
    expect(searchDestinations('catalog variants').map((d) => d.id)).toEqual([
      'catalog.variants',
    ])
    expect(searchDestinations('does-not-exist')).toEqual([])
    expect(searchDestinations('')).toHaveLength(79)
  })
})
