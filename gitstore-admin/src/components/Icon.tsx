import {
  House,
  ShoppingBag,
  Package,
  Warehouse,
  Users,
  Tags,
  FileText,
  Globe2,
  GitBranch,
  ChartNoAxesCombined,
  Blocks,
  Settings2,
  Settings,
} from 'lucide-react'
import type { NavigationGroup } from '../navigation/registry'
const icons = {
  home: House,
  orders: ShoppingBag,
  catalog: Package,
  inventory: Warehouse,
  customers: Users,
  pricing: Tags,
  content: FileText,
  channels: Globe2,
  changes: GitBranch,
  insights: ChartNoAxesCombined,
  apps: Blocks,
  platform: Settings2,
  settings: Settings,
}
export function Icon({
  name,
  size = 18,
}: {
  name: NavigationGroup['icon']
  size?: number
}) {
  const Component = icons[name]
  return <Component size={size} strokeWidth={1.65} aria-hidden="true" />
}
