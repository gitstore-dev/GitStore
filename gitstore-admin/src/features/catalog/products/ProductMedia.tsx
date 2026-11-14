import { ImageOff } from 'lucide-react'
import styles from './products.module.css'
export const demoMedia: Record<string, { src: string; alt: string }> = {
  'basin-image': {
    src: '/media/demo/basin-1.jpg',
    alt: 'Kanso ceramic basin, ash glaze detail',
  },
  lookbook: {
    src: '/media/demo/basin-2.jpg',
    alt: 'Kanso ceramic basin on a pale pedestal',
  },
}
export function ProductMedia({
  id,
  compact = false,
  priority = false,
  emptyLabel = 'No product image',
}: {
  id?: string
  compact?: boolean
  priority?: boolean
  emptyLabel?: string
}) {
  const image = id ? demoMedia[id] : undefined
  return (
    <div
      className={styles.mediaImage}
      data-compact={compact || undefined}
      data-demo-image={id}
    >
      {image ? (
        <img
          src={image.src}
          alt={image.alt}
          loading={priority ? 'eager' : 'lazy'}
          decoding={priority ? 'sync' : 'async'}
        />
      ) : (
        <span>
          <ImageOff size={compact ? 18 : 28} aria-hidden="true" />
          {!compact && emptyLabel}
        </span>
      )}
    </div>
  )
}
