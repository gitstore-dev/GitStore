import { ProductMedia } from './ProductMedia'
import styles from './card-media-backdrop.module.css'

/** Decorative full-card media; the existing thumbnail keeps the layout stable. */
export function CardMediaBackdrop({
  id,
  emptyLabel,
}: {
  id?: string
  emptyLabel?: string
}) {
  return (
    <span className={styles.backdrop} aria-hidden="true" data-card-backdrop="">
      <ProductMedia id={id} emptyLabel={emptyLabel} />
    </span>
  )
}
