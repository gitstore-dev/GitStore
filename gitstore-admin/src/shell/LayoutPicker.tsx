import { ToggleButton, Tooltip, TooltipTrigger } from 'react-aria-components'
import { Minimize2, PanelRight } from 'lucide-react'
import type { LayoutMode } from './WorkspaceLayout'
import styles from './shell.module.css'

const controls = [
  { id: 'focus', label: 'Focus mode', Icon: Minimize2 },
  { id: 'inspector', label: 'Inspector', Icon: PanelRight },
] as const

export function LayoutPicker({
  value,
  onChange,
  autoFocus = false,
  onPrepareFocus,
}: {
  value: LayoutMode
  onChange: (mode: LayoutMode) => void
  autoFocus?: boolean
  onPrepareFocus?: () => void
}) {
  return (
    <div
      role="group"
      aria-label="View controls"
      className={styles.layoutPicker}
    >
      {controls.map(({ id, label, Icon }) => (
        <TooltipTrigger key={id} delay={350}>
          <ToggleButton
            aria-label={label}
            isSelected={value === id}
            onChange={(selected) => onChange(selected ? id : 'standard')}
            autoFocus={autoFocus && id === 'focus'}
            onHoverStart={id === 'focus' ? onPrepareFocus : undefined}
            onFocus={id === 'focus' ? onPrepareFocus : undefined}
            className={styles.layoutButton}
          >
            <Icon size={18} strokeWidth={1.7} aria-hidden="true" />
          </ToggleButton>
          <Tooltip placement="bottom" className={styles.tooltip}>
            {label}
          </Tooltip>
        </TooltipTrigger>
      ))}
    </div>
  )
}
