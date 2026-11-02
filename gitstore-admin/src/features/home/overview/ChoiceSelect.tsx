import {
  Button,
  Label,
  Select,
  SelectValue,
  Popover,
  ListBox,
  ListBoxItem,
} from 'react-aria-components'
import { ChevronDown, Check } from 'lucide-react'
import styles from './overview.module.css'
export function ChoiceSelect<T extends string>({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: T
  onChange: (value: T) => void
  options: readonly { id: T; label: string }[]
}) {
  return (
    <Select
      className={styles.select}
      selectedKey={value}
      onSelectionChange={(key) => {
        const option = options.find((item) => item.id === key)
        if (option) onChange(option.id)
      }}
    >
      <Label className={styles.controlLabel}>{label}</Label>
      <Button className={styles.selectButton}>
        <SelectValue />
        <ChevronDown size={14} aria-hidden="true" />
      </Button>
      <Popover className={styles.selectPopover} placement="bottom end">
        <ListBox items={options} className={styles.options}>
          {(option) => (
            <ListBoxItem
              id={option.id}
              textValue={option.label}
              className={styles.option}
            >
              {({ isSelected }) => (
                <>
                  <span>{option.label}</span>
                  {isSelected && <Check size={14} aria-hidden="true" />}
                </>
              )}
            </ListBoxItem>
          )}
        </ListBox>
      </Popover>
    </Select>
  )
}
