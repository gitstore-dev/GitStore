import React from 'react';

interface Conflict {
  field: string;
  currentValue: string;
  incomingValue: string;
  diff?: string;
}

interface ConflictModalProps {
  conflict: Conflict;
  onResolve: (resolution: 'overwrite' | 'cancel') => void;
}

/**
 * Conflict resolution modal for optimistic locking
 * Shows when a concurrent modification is detected
 */
export function ConflictModal({ conflict, onResolve }: ConflictModalProps) {
  return (
    <div style={styles.overlay} onClick={() => onResolve('cancel')}>
      <div style={styles.modal} onClick={(e) => e.stopPropagation()}>
        <div style={styles.header}>
          <h2 style={styles.title}>Conflict Detected</h2>
          <p style={styles.subtitle}>
            This product was modified by another user while you were editing.
          </p>
        </div>

        <div style={styles.body}>
          <div style={styles.conflictSection}>
            <div style={styles.label}>Field</div>
            <div style={styles.value}>{conflict.field}</div>
          </div>

          <div style={styles.conflictSection}>
            <div style={styles.label}>Current Value (in database)</div>
            <div style={styles.valueBox}>
              <code style={styles.code}>{conflict.currentValue}</code>
            </div>
          </div>

          <div style={styles.conflictSection}>
            <div style={styles.label}>Your Value</div>
            <div style={styles.valueBox}>
              <code style={styles.code}>{conflict.incomingValue}</code>
            </div>
          </div>

          {conflict.diff && (
            <div style={styles.conflictSection}>
              <div style={styles.label}>Diff</div>
              <pre style={styles.diff}>{conflict.diff}</pre>
            </div>
          )}

          <div style={styles.warningBox}>
            <strong>⚠️ Warning:</strong> Overwriting will discard the other user's changes.
          </div>
        </div>

        <div style={styles.footer}>
          <button
            onClick={() => onResolve('cancel')}
            style={styles.cancelButton}
          >
            Cancel & Reload
          </button>
          <button
            onClick={() => onResolve('overwrite')}
            style={styles.overwriteButton}
          >
            Overwrite Changes
          </button>
        </div>
      </div>
    </div>
  );
}

const styles = {
  overlay: {
    position: 'fixed',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    zIndex: 1000,
  } as React.CSSProperties,
  modal: {
    backgroundColor: 'white',
    borderRadius: '8px',
    maxWidth: '600px',
    width: '90%',
    maxHeight: '80vh',
    overflow: 'auto',
    boxShadow: '0 20px 25px -5px rgba(0, 0, 0, 0.1), 0 10px 10px -5px rgba(0, 0, 0, 0.04)',
  } as React.CSSProperties,
  header: {
    padding: '1.5rem',
    borderBottom: '1px solid #e2e8f0',
  } as React.CSSProperties,
  title: {
    margin: '0 0 0.5rem',
    fontSize: '1.5rem',
    fontWeight: 600,
    color: '#1a202c',
  } as React.CSSProperties,
  subtitle: {
    margin: 0,
    fontSize: '0.875rem',
    color: '#718096',
  } as React.CSSProperties,
  body: {
    padding: '1.5rem',
  } as React.CSSProperties,
  conflictSection: {
    marginBottom: '1.5rem',
  } as React.CSSProperties,
  label: {
    marginBottom: '0.5rem',
    fontSize: '0.875rem',
    fontWeight: 600,
    color: '#4a5568',
    textTransform: 'uppercase',
    letterSpacing: '0.05em',
  } as React.CSSProperties,
  value: {
    fontSize: '1rem',
    color: '#1a202c',
  } as React.CSSProperties,
  valueBox: {
    padding: '0.75rem',
    backgroundColor: '#f7fafc',
    borderRadius: '4px',
    border: '1px solid #e2e8f0',
  } as React.CSSProperties,
  code: {
    fontSize: '0.875rem',
    color: '#1a202c',
    fontFamily: 'monospace',
  } as React.CSSProperties,
  diff: {
    padding: '0.75rem',
    backgroundColor: '#1a202c',
    color: '#48bb78',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontFamily: 'monospace',
    overflow: 'auto',
    margin: 0,
  } as React.CSSProperties,
  warningBox: {
    padding: '1rem',
    backgroundColor: '#fef5e7',
    border: '1px solid #f39c12',
    borderRadius: '4px',
    fontSize: '0.875rem',
    color: '#7d6608',
  } as React.CSSProperties,
  footer: {
    padding: '1.5rem',
    borderTop: '1px solid #e2e8f0',
    display: 'flex',
    justifyContent: 'flex-end',
    gap: '1rem',
  } as React.CSSProperties,
  cancelButton: {
    padding: '0.75rem 1.5rem',
    backgroundColor: 'transparent',
    color: '#718096',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '1rem',
    fontWeight: 500,
    cursor: 'pointer',
    transition: 'all 0.2s',
  } as React.CSSProperties,
  overwriteButton: {
    padding: '0.75rem 1.5rem',
    backgroundColor: '#e53e3e',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    fontSize: '1rem',
    fontWeight: 500,
    cursor: 'pointer',
    transition: 'background 0.2s',
  } as React.CSSProperties,
};
