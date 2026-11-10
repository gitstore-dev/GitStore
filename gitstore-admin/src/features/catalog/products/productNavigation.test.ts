import { describe, expect, it } from 'vitest'
import {
  productJourney,
  productRoute,
  ProductNavigation,
} from './productNavigation'
import { productSearch } from './model'
const base = '/w/atelier-kyoto/catalog/products'
describe('Product navigation boundaries', () => {
  it('pairs list/detail in both directions without treating edit/create as detail', () => {
    expect(productJourney(base, `${base}/stone-basin`)).toEqual({
      namespace: 'atelier-kyoto',
      id: 'stone-basin',
      direction: 'open',
    })
    expect(productJourney(`${base}/stone-basin/`, base)).toMatchObject({
      direction: 'return',
    })
    expect(productRoute(`${base}/new`)).toBeUndefined()
    expect(productJourney(base, `${base}/stone-basin/edit`)).toBeUndefined()
    expect(
      productJourney(`${base}/stone-basin`, `${base}/silk-tunic`),
    ).toBeUndefined()
    expect(
      productJourney(base, '/w/studio-berlin/catalog/products/stone-basin'),
    ).toBeUndefined()
    expect(productJourney('/platform/namespaces', base)).toBeUndefined()
  })
  it('retains loaded batches by list URL and invalidates them on store reset', () => {
    const navigation = new ProductNavigation()
    navigation.remember(`${base}?q=stone`, {
      epoch: 3,
      filterKey: 'stone',
      cursors: ['demo-product:stone-basin'],
      search: productSearch({ q: 'stone' }),
      scroll: 0,
    })
    expect(navigation.position(`${base}?q=stone`, 3)?.cursors).toEqual([
      'demo-product:stone-basin',
    ])
    expect(navigation.position(`${base}?q=stone`, 4)).toBeUndefined()
    expect(navigation.position(base, 3)).toBeUndefined()
  })
  it('bounds retained list state rather than retaining every filter combination', () => {
    const navigation = new ProductNavigation()
    for (let i = 0; i < 40; i++)
      navigation.remember(`${base}?q=${i}`, {
        epoch: 0,
        filterKey: String(i),
        cursors: [],
        search: productSearch({ q: String(i) }),
        scroll: 0,
      })
    expect(navigation.position(`${base}?q=0`, 0)).toBeUndefined()
    expect(navigation.position(`${base}?q=39`, 0)).toBeDefined()
  })
})
