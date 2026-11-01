import { Link, useParams } from '@tanstack/react-router'
import { ArrowUpRight, Command, CornerDownLeft } from 'lucide-react'
import { destinationById, destinations } from '../navigation/registry'
import { usePreferences } from '../state/preferences'
import styles from './placeholder.module.css'
export function PlaceholderPage({ destinationId }: { destinationId: string }) {
  const destination = destinationById(destinationId)
  const preferences = usePreferences()
  const params = useParams({ strict: false })
  const namespace =
    'namespace' in params
      ? (params.namespace ?? preferences.namespace)
      : preferences.namespace
  const next = destinations[destination.ordinal % destinations.length]!
  return (
    <div className={styles.page}>
      <div className={styles.pageHeading}>
        <div>
          <div className={styles.eyebrow}>
            {destination.groupLabel}{' '}
            <span>/ {String(destination.ordinal).padStart(2, '0')}</span>
          </div>
          <h1>{destination.label}</h1>
          <p>{destination.description}</p>
        </div>
        <span className={styles.status}>
          <span />
          FOUNDATION
        </span>
      </div>
      <section className={styles.canvas} aria-labelledby="placeholder-heading">
        <div className={styles.canvasTop}>
          <span>ROOM TO BUILD</span>
          <span className={styles.canvasIndex}>
            {String(destination.ordinal).padStart(2, '0')} / 79
          </span>
        </div>
        <div className={styles.placeholderBody}>
          <div className={styles.art} aria-hidden="true">
            <svg viewBox="0 0 400 280">
              <defs>
                <pattern
                  id="dot-grid"
                  x="0"
                  y="0"
                  width="16"
                  height="16"
                  patternUnits="userSpaceOnUse"
                >
                  <circle
                    cx="1"
                    cy="1"
                    r=".8"
                    fill="currentColor"
                    opacity=".2"
                  />
                </pattern>
              </defs>
              <rect width="400" height="280" fill="url(#dot-grid)" />
              <path
                d="M85 95 200 42 320 95 205 149Z"
                className={styles.slabTop}
              />
              <path
                d="M85 95v27l120 55 115-55V95l-115 54Z"
                className={styles.slabSide}
              />
              <path
                d="m85 145 120 55 115-55v27l-115 55-120-55Z"
                className={styles.slabBottom}
              />
              <path
                d="M200 42v54l-46 23 51 24v56l53-25"
                className={styles.returnPath}
              />
              <path d="m245 160 17 12-5 19" className={styles.returnPath} />
              <rect
                x="188"
                y="186"
                width="33"
                height="24"
                rx="2"
                className={styles.node}
              />
            </svg>
          </div>
          <span className={styles.placeholderLabel}>PAGE NOT IMPLEMENTED</span>
          <h2 id="placeholder-heading">
            Your next page
            <br />
            starts here<span>.</span>
          </h2>
          <p>
            Page not implemented. The shell is ready —
            <br className={styles.desktopBreak} /> this space is reserved for{' '}
            {destination.label.toLowerCase()}.
          </p>
          <Link className={styles.nextLink} to={next.to} params={{ namespace }}>
            Explore next destination
            <ArrowUpRight size={15} aria-hidden="true" />
          </Link>
        </div>
        <div className={styles.canvasBottom}>
          <span>
            <span className={styles.smallDot} />
            {destination.scope === 'platform'
              ? 'Platform scope'
              : 'Demo workspace'}
          </span>
          <span>
            PREPARE <span className={styles.slash}>/</span> REVIEW{' '}
            <span className={styles.slash}>/</span> RECOVER
          </span>
        </div>
      </section>
      <footer className={styles.pageFooter}>
        <span>Made for considered commerce.</span>
        <span>
          <Command size={12} aria-hidden="true" />
          <kbd>K</kbd> to find your way{' '}
          <CornerDownLeft size={12} aria-hidden="true" />
        </span>
      </footer>
    </div>
  )
}
export function NotFoundPage() {
  const { namespace } = usePreferences()
  return (
    <div className={styles.page}>
      <div className={styles.pageHeading}>
        <div>
          <span className={styles.eyebrow}>A SMALL DETOUR / 404</span>
          <h1>Nothing here, yet.</h1>
          <p>This address isn’t part of the workspace.</p>
        </div>
      </div>
      <section className={styles.notFound}>
        <h2>Let’s find your way back.</h2>
        <Link to="/w/$namespace/home/overview" params={{ namespace }}>
          Back to Overview <ArrowUpRight size={16} aria-hidden="true" />
        </Link>
      </section>
    </div>
  )
}
