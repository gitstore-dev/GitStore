import { ImageOff } from 'lucide-react'
import { useState } from 'react'
import styles from './media-preview.module.css'
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
export function MediaPreview({
  id,
  compact = false,
  priority = false,
  emptyLabel = 'No product image',
  broken = false,
}: {
  id?: string
  compact?: boolean
  priority?: boolean
  emptyLabel?: string
  broken?: boolean
}) {
  const image = id ? demoMedia[id] : undefined
  return (
    <PreviewImage
      key={`${id}-${broken}`}
      image={image}
      id={id}
      compact={compact}
      priority={priority}
      emptyLabel={emptyLabel}
      broken={broken}
    />
  )
}
function PreviewImage({
  image,
  id,
  compact,
  priority,
  emptyLabel,
  broken,
}: {
  image?: { src: string; alt: string }
  id?: string
  compact: boolean
  priority: boolean
  emptyLabel: string
  broken: boolean
}) {
  const [failed, setFailed] = useState(false)
  return (
    <div
      className={styles.mediaImage}
      data-media-preview=""
      data-compact={compact || undefined}
      data-demo-image={id}
    >
      {image && !failed ? (
        <img
          src={broken ? '/media/demo/unavailable-preview.jpg' : image.src}
          onError={() => setFailed(true)}
          alt={image.alt}
          loading={priority ? 'eager' : 'lazy'}
          decoding={priority ? 'sync' : 'async'}
        />
      ) : (
        <span>
          <ImageOff size={compact ? 18 : 28} aria-hidden="true" />
          {!compact && (failed ? 'Preview unavailable' : emptyLabel)}
        </span>
      )}
    </div>
  )
}
