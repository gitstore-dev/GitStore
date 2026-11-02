import type { Theme, MotionPreference } from '../state/preferences'
import type { LayoutMode } from '../shell/WorkspaceLayout'
import type { OverviewScenario } from '../features/home/overview/model'
export interface AppRouterContext {
  preferences?: {
    initialTheme?: Theme
    initialMotion?: MotionPreference
    initialCollapsed?: boolean
  }
  initialLayout?: LayoutMode
  /** Isolated presentation scenarios for Storybook; never read from a URL. */
  overviewScenario?: OverviewScenario
  overviewLighting?: 'auto' | 'css'
}
