import { ProductMedia } from './ProductMedia'
import styles from './card-media-backdrop.module.css'

/** Decorative full-card media; the existing thumbnail keeps the layout stable. */
export function CardMediaBackdrop({ id }: { id?: string }) {
  return (
    <span className={styles.backdrop} aria-hidden="true" data-card-backdrop="">
      <ProductMedia id={id} />
    </span>
  )
}
