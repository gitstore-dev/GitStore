import { Button, Radio, RadioGroup } from 'react-aria-components'
import type { ButtonProps } from 'react-aria-components'
import { Sun, Moon, Monitor } from 'lucide-react'
import { usePreferences } from '../state/preferences'
import type { Theme } from '../state/preferences'
import styles from './controls.module.css'
export function ActionButton({
  variant = 'secondary',
  className = '',
  ...props
}: Omit<ButtonProps, 'className'> & {
  variant?: 'primary' | 'secondary' | 'ghost'
  className?: string
}) {
  return (
    <Button
      {...props}
      className={`${styles.button} ${styles[variant]} ${className}`}
    />
  )
}
export function ThemePicker() {
  const { theme, setTheme } = usePreferences()
  return (
    <RadioGroup
      aria-label="Theme"
      orientation="horizontal"
      value={theme}
      onChange={(value) => setTheme(value as Theme)}
      className={styles.themes}
    >
      {(
        [
          { value: 'light', label: 'Light', Icon: Sun },
          { value: 'dark', label: 'Dark', Icon: Moon },
          { value: 'system', label: 'System', Icon: Monitor },
        ] as const
      ).map(({ value, label, Icon }) => (
        <Radio key={value} value={value} className={styles.theme}>
          <Icon size={14} aria-hidden="true" />
          <span>{label}</span>
        </Radio>
      ))}
    </RadioGroup>
  )
}
