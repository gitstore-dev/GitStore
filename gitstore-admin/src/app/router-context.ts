import type { StructureScenario } from '../features/catalog/structure/model'
import type { MediaScenario } from '../features/catalog/media/model'
import type { CatalogScenario } from '../features/catalog/browse/model'
import type { SetupScenario } from '../features/home/setup-guide/model'
import type { Theme, MotionPreference } from '../state/preferences'
import type { LayoutMode } from '../shell/WorkspaceLayout'
import type { OverviewScenario } from '../features/home/overview/model'
import type { WorkScenario, WorkTab } from '../features/home/my-work/model'
import type { ActivityScenario, ProductScenario } from '../features/demo/model'
import type { ProductNavigation } from '../features/catalog/products/productNavigation'
export interface AppRouterContext {
  structureScenario?: StructureScenario
  mediaScenario?: MediaScenario
  catalogScenario?: CatalogScenario
  setupScenario?: SetupScenario
  productNavigation?: ProductNavigation
  preferences?: {
    initialTheme?: Theme
    initialMotion?: MotionPreference
    initialCollapsed?: boolean
  }
  initialLayout?: LayoutMode
  /** Isolated presentation scenarios for Storybook; never read from a URL. */
  overviewScenario?: OverviewScenario
  overviewLighting?: 'auto' | 'css'
  myWorkScenario?: WorkScenario
  myWorkTab?: WorkTab
  activityScenario?: ActivityScenario
  productScenario?: ProductScenario
}
