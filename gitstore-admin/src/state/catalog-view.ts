import { useEffect } from 'react'
type Domain = 'products' | 'variants' | 'categories' | 'collections'
export function readCatalogView(domain: Domain) {
  try {
    const view = localStorage.getItem(`gitstore.view.${domain}`)
    return (domain === 'categories'
      ? ['tree', 'table', 'spatial']
      : ['table', 'grid']
    ).includes(view ?? '')
      ? view
      : undefined
  } catch {
    return undefined
  }
}
export function rememberCatalogView(domain: Domain, view: string) {
  try {
    localStorage.setItem(`gitstore.view.${domain}`, view)
  } catch {
    /* UI preferences remain optional. */
  }
}
export function useCatalogView(domain: Domain, view: string) {
  useEffect(() => rememberCatalogView(domain, view), [domain, view])
}
