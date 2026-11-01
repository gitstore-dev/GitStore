import {
  Button,
  Select,
  SelectValue,
  Popover,
  ListBox,
  ListBoxItem,
  Label,
} from 'react-aria-components'
import { ChevronDown, Check, Layers } from 'lucide-react'
import { namespaces } from '../data/demo'
import type { NamespaceId } from '../data/demo'
import styles from '../shell/shell.module.css'
export function NamespacePicker({
  value,
  onChange,
  compact = false,
  platform = false,
  onPlatformChange,
}: {
  value: NamespaceId
  onChange: (id: NamespaceId) => void
  compact?: boolean
  platform?: boolean
  onPlatformChange?: () => void
}) {
  const choices = onPlatformChange
    ? [
        ...namespaces,
        {
          id: 'platform',
          name: 'Control plane',
          description: 'Platform workspace · outside namespaces',
        },
      ]
    : namespaces
  const selected =
    choices.find((choice) => choice.id === (platform ? 'platform' : value)) ??
    namespaces[0]
  return (
    <Select
      className={styles.workspaceSelect}
      selectedKey={platform ? 'platform' : value}
      onSelectionChange={(key) =>
        key === 'platform' ? onPlatformChange?.() : onChange(key as NamespaceId)
      }
    >
      <Label className="sr-only">Workspace context</Label>
      <Button
        className={`${styles.workspaceButton} ${compact ? styles.workspaceCompact : ''}`}
      >
        <span aria-hidden="true" className={styles.workspaceAvatar}>
          <Layers size={16} aria-hidden="true" />
        </span>{' '}
        {compact && (
          <SelectValue className="sr-only">{selected.name}</SelectValue>
        )}
        {!compact && (
          <>
            <span className={styles.workspaceName}>
              <SelectValue>{selected.name}</SelectValue>
            </span>
            <ChevronDown size={14} aria-hidden="true" />
          </>
        )}
      </Button>
      <Popover
        placement={compact ? 'right top' : 'bottom start'}
        offset={5}
        className={`${styles.popover} ${compact ? '' : styles.workspacePopover}`}
      >
        <ListBox items={choices} className={styles.menu}>
          {(namespace) => (
            <ListBoxItem
              id={namespace.id}
              textValue={namespace.name}
              className={styles.menuItem}
            >
              {({ isSelected }) => (
                <>
                  <span>
                    <strong>{namespace.name}</strong>
                    <small>{namespace.description}</small>
                  </span>
                  {isSelected && (
                    <Check
                      size={15}
                      aria-hidden="true"
                      data-selection-indicator
                    />
                  )}
                </>
              )}
            </ListBoxItem>
          )}
        </ListBox>
      </Popover>
    </Select>
  )
}
