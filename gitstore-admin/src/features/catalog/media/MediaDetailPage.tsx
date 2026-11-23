import { useLayoutEffect, useMemo, useRef } from 'react'
import { Link, useLocation, useRouter, useSearch } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import { namespaces } from '../../../data/demo'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import { HomePage, PageContext, DemoControls } from '../../home/shared/HomePage'
import { EmptyState } from '../../home/shared/EmptyState'
import { TechnicalDetails } from '../../../components/TechnicalDetails'
import { MarkdownPreview } from '../../../components/markdown/MarkdownPreview'
import { usePageInspector } from '../../../shell/PageInspector'
import { StatusBadge } from '../../home/overview/StatusBadge'
import { MediaPreview } from '../shared/MediaPreview'
import { MediaViewer } from './MediaViewer'
import { mediaEntries, mediaSearch } from './model'
import type { MediaUsage, MediaSearch } from './model'
import catalog from '../browse/catalog.module.css'
import styles from './media.module.css'
export function UsageLink({
  usage,
  namespace,
}: {
  usage: MediaUsage
  namespace: NamespaceId
}) {
  if (usage.kind === 'Product')
    return (
      <Link
        to="/w/$namespace/catalog/products/$productId"
        params={{ namespace, productId: usage.id }}
      >
        {usage.title}
      </Link>
    )
  const to =
    usage.kind === 'ProductVariant'
      ? '/w/$namespace/catalog/variants/$itemId'
      : '/w/$namespace/catalog/categories/$itemId'
  return (
    <Link to={to} params={{ namespace, itemId: usage.id }}>
      {usage.title}
    </Link>
  )
}
export function MediaDetailPage({
  namespace,
  itemId,
}: {
  namespace: NamespaceId
  itemId: string
}) {
  const { session, reset } = useHomeDemo(namespace),
    router = useRouter(),
    location = useLocation()
  const scenario = router.options.context.mediaScenario
  const entry = useMemo(
    () => mediaEntries(session, scenario).find((item) => item.id === itemId),
    [session, scenario, itemId],
  )
  const rawSearch = useSearch({ strict: false })
  const search =
    router.options.context.productNavigation?.returnSearch<MediaSearch>(
      location.pathname,
    ) ?? mediaSearch(rawSearch)
  const page = useRef<HTMLDivElement>(null)
  const inspector = useMemo(
    () =>
      entry ? (
        <div className={catalog.inspector}>
          <h2>{entry.name}</h2>
          <StatusBadge state={entry.status} />
          <p>{entry.diagnostic}</p>
          <p>
            Read-only demo file. Available previews do not imply completed
            processing or publication.
          </p>
        </div>
      ) : null,
    [entry],
  )
  usePageInspector(inspector)
  useLayoutEffect(() => {
    router.options.context.productNavigation?.bind(page.current)
    router.options.context.productNavigation?.mounted(
      location.href,
      session.epoch,
    )
  })
  return (
    <HomePage ref={page} className={styles.page}>
      <div className={catalog.top}>
        <Link
          className={catalog.back}
          to="/w/$namespace/catalog/media"
          params={{ namespace }}
          search={search}
          resetScroll={false}
        >
          <ArrowLeft size={15} aria-hidden="true" /> Media
        </Link>
        <DemoControls
          onReset={reset}
          details="Example images and file details for this demo. Uploading, editing and processing files are unavailable."
        />
      </div>
      {!entry || scenario === 'unavailable' || scenario === 'loading' ? (
        <EmptyState
          title={scenario === 'loading' ? 'Loading media' : 'Media unavailable'}
        >
          This file cannot be inspected in the current demo context.
        </EmptyState>
      ) : (
        <article className={catalog.detail} data-product-surface={itemId}>
          <div className={catalog.detailContent} data-product-content="detail">
            <PageContext
              namespace={namespaces.find((n) => n.id === namespace)!.name}
              section="Media file"
            />
            <h1 tabIndex={-1} data-product-title="">
              {entry.name}
            </h1>
            <StatusBadge state={entry.status} />
            <p className={catalog.note}>{entry.diagnostic}</p>
            {scenario === 'stale' && (
              <p className={catalog.warning}>
                Earlier demo snapshot. This is not live media data.
              </p>
            )}
            <div className={styles.detailGrid}>
              <div>
                <div className={styles.hero} data-product-image="">
                  <MediaPreview
                    id={entry.details.previewId}
                    broken={entry.broken}
                    priority
                    emptyLabel="No media preview"
                  />
                </div>
                <MediaViewer entry={entry} />
                <p className={catalog.note}>Example image for this demo.</p>
              </div>
              <div>
                <h2>Used by</h2>
                {entry.usage.length ? (
                  <ul className={styles.usage}>
                    {entry.usage.map((usage) => (
                      <li key={usage.id}>
                        <UsageLink usage={usage} namespace={namespace} />
                        <small>
                          {usage.kind === 'CategoryTaxonomy'
                            ? 'Category'
                            : usage.kind === 'ProductVariant'
                              ? 'Product variant'
                              : 'Product'}
                        </small>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className={catalog.note}>
                    Not used by any items in this demo.
                  </p>
                )}
              </div>
            </div>
            {entry.details.body && (
              <section>
                <h2>About this file</h2>
                <MarkdownPreview source={entry.details.body} />
              </section>
            )}
            <section>
              <h2>Image processing</h2>
              <p className={catalog.note}>
                These are example requests and results; no images are generated
                in this demo.
              </p>
              <div className={styles.processing}>
                <div>
                  <h3>Requested sizes</h3>
                  {entry.details.requests.length ? (
                    <ul>
                      {entry.details.requests.map((name) => (
                        <li key={name}>{name}</li>
                      ))}
                    </ul>
                  ) : (
                    <p>No image sizes requested.</p>
                  )}
                </div>
                <div>
                  <h3>Available demo outputs</h3>
                  {entry.details.outputs.length ? (
                    <ul>
                      {entry.details.outputs.map((output) => (
                        <li key={output.name}>
                          {output.name}
                          {output.previewId && (
                            <MediaPreview
                              id={output.previewId}
                              compact
                              emptyLabel="Output preview unavailable"
                            />
                          )}
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <p>No output recorded in this demo.</p>
                  )}
                </div>
              </div>
            </section>
            <TechnicalDetails resourceType="File" sourcePath={entry.path}>
              <dl>
                <dt>Declared content type</dt>
                <dd>{entry.details.contentType}</dd>
                <dt>Provider</dt>
                <dd>{entry.details.provider}</dd>
                <dt>Source URI</dt>
                <dd>
                  <code>{entry.details.sourceUri}</code>
                </dd>
                {entry.details.checksum && (
                  <>
                    <dt>Checksum algorithm</dt>
                    <dd>{entry.details.checksum.algorithm}</dd>
                    <dt>Checksum</dt>
                    <dd>
                      <code>{entry.details.checksum.value}</code>
                    </dd>
                  </>
                )}
              </dl>
            </TechnicalDetails>
          </div>
        </article>
      )}
    </HomePage>
  )
}
