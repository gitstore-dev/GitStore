import type { ReactNode } from 'react'
import { Dialog, Heading, Modal, ModalOverlay } from 'react-aria-components'
import { ActionButton } from './controls'
import styles from './filter-sheet.module.css'
export function FilterSheet({
  open,
  onClose,
  onApply,
  onClear,
  count,
  children,
  title,
  itemLabel,
  applyLabel,
}: {
  title: string
  open: boolean
  onClose: () => void
  onApply: () => void
  onClear: () => void
  children: ReactNode
} & (
  | { applyLabel: string; count?: never; itemLabel?: never }
  | { applyLabel?: never; count: number; itemLabel: string }
)) {
  return (
    <ModalOverlay
      isOpen={open}
      onOpenChange={(value) => {
        if (!value) onClose()
      }}
      isDismissable
      className={styles.sheetOverlay}
    >
      <Modal className={styles.sheet}>
        <Dialog className={styles.sheetDialog}>
          <Heading slot="title">{title}</Heading>
          <div className={styles.sheetFields}>{children}</div>
          <div className={styles.sheetActions}>
            <ActionButton onPress={onClear}>Clear filters</ActionButton>
            <ActionButton onPress={onClose}>Cancel</ActionButton>
            <ActionButton variant="primary" onPress={onApply}>
              {applyLabel ?? `Show ${count} ${itemLabel}`}
            </ActionButton>
          </div>
        </Dialog>
      </Modal>
    </ModalOverlay>
  )
}
