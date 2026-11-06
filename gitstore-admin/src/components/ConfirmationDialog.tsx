import type { ReactNode } from 'react'
import { ModalOverlay, Modal, Dialog } from 'react-aria-components'
import styles from './confirmation-dialog.module.css'
/** Confirmations always sit above drawers, regardless of which page owns them. */
export function ConfirmationDialog({
  isOpen,
  onOpenChange,
  children,
}: {
  isOpen: boolean
  onOpenChange: (open: boolean) => void
  children: ReactNode
}) {
  return (
    <ModalOverlay
      isOpen={isOpen}
      onOpenChange={onOpenChange}
      isDismissable
      className={styles.overlay}
    >
      <Modal className={styles.modal}>
        <Dialog className={styles.dialog}>{children}</Dialog>
      </Modal>
    </ModalOverlay>
  )
}
