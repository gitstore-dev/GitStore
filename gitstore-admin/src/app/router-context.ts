import type { Theme, MotionPreference } from '../state/preferences'
import type { LayoutMode } from '../shell/WorkspaceLayout'
export interface AppRouterContext {
  preferences?: {
    initialTheme?: Theme
    initialMotion?: MotionPreference
    initialCollapsed?: boolean
  }
  initialLayout?: LayoutMode
}
