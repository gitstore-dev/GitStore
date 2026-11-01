export const namespaces = [
  {
    id: 'atelier-kyoto',
    name: 'Atelier Kyoto',
    initials: 'AK',
    description: 'Independent objects & thoughtful commerce',
  },
  {
    id: 'studio-berlin',
    name: 'Studio Berlin',
    initials: 'SB',
    description: 'A second space to make your own',
  },
] as const
export type NamespaceId = (typeof namespaces)[number]['id']
export const defaultNamespace: NamespaceId = 'atelier-kyoto'
export const demoUser = {
  name: 'Elena Rostova',
  initials: 'ER',
  role: 'Demo operator',
} as const
export function isDemoNamespace(value: string): value is NamespaceId {
  return namespaces.some((namespace) => namespace.id === value)
}
