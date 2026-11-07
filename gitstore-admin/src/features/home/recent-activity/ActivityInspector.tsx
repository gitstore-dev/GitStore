import { ActionButton } from '../../../components/controls'
import { TechnicalDetails } from '../../../components/TechnicalDetails'
import { ChangePreview } from '../../demo/ChangePreview'
import { snapshotChanges, snapshotTitle } from '../../demo/snapshots'
import { restoreProblem } from '../../demo/model'
import type { ActivityEvent, DemoSession } from '../../demo/model'
import { kindLabels } from '../overview/presentation'
import { formatWorkTime } from '../my-work/WorkInspector'
import styles from './recent-activity.module.css'
export function ActivityInspector({
  event,
  session,
  onRestore,
}: {
  event: ActivityEvent
  session: DemoSession
  onRestore: () => void
}) {
  const resource = session.resources.find((item) => item.id === event.itemId)
  const current = session.catalog[event.itemId]
  const problem = restoreProblem(session, event.id)
  return (
    <div className={styles.details}>
      <span className={styles.kicker}>
        {kindLabels[event.kind].singular} ·{' '}
        {event.local ? 'DEMO SESSION' : 'DEMO HISTORY'}
      </span>
      <h2 id="activity-inspector-heading" tabIndex={-1}>
        {event.itemTitle}
      </h2>
      <p>{event.label}</p>
      <dl className={styles.metadata}>
        <dt>Actor</dt>
        <dd>{event.actor}</dd>
        <dt>Recorded</dt>
        <dd>
          <time dateTime={event.timestamp}>
            {formatWorkTime(event.timestamp)}
          </time>
        </dd>
      </dl>
      <p>{event.description}</p>
      {event.sourceEventId && (
        <p className={styles.note}>
          Recovered from an earlier recorded version. The original event is
          preserved.
        </p>
      )}
      {event.after ? (
        <ChangePreview
          changes={snapshotChanges(event.before, event.after, session.catalog)}
          label="Recorded changes"
        />
      ) : (
        <>
          {event.changes && (
            <ChangePreview changes={event.changes} label="Recorded changes" />
          )}
          <p className={styles.note}>
            This event did not record an authored catalog version. It cannot be
            used to restore an item.
          </p>
        </>
      )}
      {current && (
        <section className={styles.note}>
          <h3>Current demo item</h3>
          <p>{snapshotTitle(current)}</p>
          <p>
            Historical outcomes do not describe current readiness or
            publication.
          </p>
        </section>
      )}
      {event.after && (
        <>
          <ActionButton
            variant="primary"
            isDisabled={!!problem}
            onPress={onRestore}
          >
            Restore this version in demo
          </ActionButton>
          {problem && <p className={styles.note}>{problem}</p>}
        </>
      )}
      {resource && (
        <TechnicalDetails
          resourceType={event.kind}
          sourcePath={resource.path}
        />
      )}
      <p className={styles.muted}>
        Demo history only. Your store and saved files are not changed.
      </p>
    </div>
  )
}
