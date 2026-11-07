import type { ReactNode } from 'react'
import { Dialog, Heading, Modal, ModalOverlay } from 'react-aria-components'
import { ActionButton } from '../../../components/controls'
import styles from './recent-activity.module.css'
export function ActivityFiltersSheet({
  open,
  onClose,
  onApply,
  onClear,
  count,
  children,
}: {
  open: boolean
  onClose: () => void
  onApply: () => void
  onClear: () => void
  count: number
  children: ReactNode
}) {
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
          <Heading slot="title">Filter activity</Heading>
          <div className={styles.sheetFields}>{children}</div>
          <div className={styles.sheetActions}>
            <ActionButton onPress={onClear}>Clear filters</ActionButton>
            <ActionButton onPress={onClose}>Cancel</ActionButton>
            <ActionButton variant="primary" onPress={onApply}>
              Show {count} {count === 1 ? 'event' : 'events'}
            </ActionButton>
          </div>
        </Dialog>
      </Modal>
    </ModalOverlay>
  )
}
