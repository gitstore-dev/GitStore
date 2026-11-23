import { useState } from 'react'
import {
  Dialog,
  DialogTrigger,
  Heading,
  Modal,
  ModalOverlay,
} from 'react-aria-components'
import { Expand, X } from 'lucide-react'
import { ActionButton } from '../../../components/controls'
import { demoMedia } from '../shared/MediaPreview'
import type { MediaEntry } from './model'
import styles from './media.module.css'
export function MediaViewer({ entry }: { entry: MediaEntry }) {
  const [open, setOpen] = useState(false),
    [actual, setActual] = useState(false),
    [failed, setFailed] = useState(false)
  const image = entry.details.previewId
    ? demoMedia[entry.details.previewId]
    : undefined
  if (!image || entry.broken)
    return (
      <p className={styles.note}>
        An enlarged preview is unavailable. File details remain accessible.
      </p>
    )
  return (
    <DialogTrigger
      isOpen={open}
      onOpenChange={(value) => {
        setOpen(value)
        setActual(false)
        setFailed(false)
      }}
    >
      <ActionButton variant="ghost">
        <Expand size={16} aria-hidden="true" />
        Enlarge preview
      </ActionButton>
      <ModalOverlay className={styles.previewOverlay} isDismissable>
        <Modal className={styles.previewModal}>
          <Dialog className={styles.previewDialog}>
            <div className={styles.viewerTools}>
              <Heading slot="title">Preview {entry.name}</Heading>
              <div>
                <ActionButton
                  aria-pressed={!actual}
                  onPress={() => setActual(false)}
                >
                  Fit
                </ActionButton>
                <ActionButton
                  aria-pressed={actual}
                  onPress={() => setActual(true)}
                >
                  100%
                </ActionButton>
                <ActionButton
                  aria-label="Close preview"
                  onPress={() => setOpen(false)}
                >
                  <X size={18} aria-hidden="true" />
                </ActionButton>
              </div>
            </div>
            <div
              className={styles.viewerImage}
              role="region"
              aria-label="Image preview"
              tabIndex={0}
              data-actual={actual || undefined}
            >
              {failed ? (
                <p role="status">
                  Preview unavailable. Close this dialog to inspect file
                  details.
                </p>
              ) : (
                <img
                  src={image.src}
                  alt={image.alt}
                  onError={() => setFailed(true)}
                />
              )}
            </div>
            <p className={styles.note}>
              100% shows the example image at its original size.
            </p>
          </Dialog>
        </Modal>
      </ModalOverlay>
    </DialogTrigger>
  )
}
