import React, { useState } from 'react';

interface PublishModalProps {
  isOpen: boolean;
  onClose: () => void;
  onConfirm: (version: string, message: string) => void | Promise<void>;
  isPublishing?: boolean;
}

/**
 * Modal for catalog publishing with version input and confirmation
 */
export function PublishModal({ isOpen, onClose, onConfirm, isPublishing = false }: PublishModalProps) {
  const [version, setVersion] = useState('');
  const [message, setMessage] = useState('');
  const [useAutoVersion, setUseAutoVersion] = useState(true);
  const [errors, setErrors] = useState<Record<string, string>>({});

  if (!isOpen) {
    return null;
  }

  const handleClose = () => {
    if (!isPublishing) {
      setVersion('');
      setMessage('');
      setUseAutoVersion(true);
      setErrors({});
      onClose();
    }
  };

  const validate = (): boolean => {
    const newErrors: Record<string, string> = {};

    if (!useAutoVersion) {
      if (!version.trim()) {
        newErrors.version = 'Version is required when not using auto-versioning';
      } else if (!/^v?\d+\.\d+\.\d+$/.test(version.trim())) {
        newErrors.version = 'Version must be in semver format (e.g., 1.0.0 or v1.0.0)';
      }
    }

    if (!message.trim()) {
      newErrors.message = 'Release message is required';
    } else if (message.trim().length < 10) {
      newErrors.message = 'Release message must be at least 10 characters';
    }

    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    if (!validate()) {
      return;
    }

    // Generate auto-version if enabled (use timestamp-based version)
    const finalVersion = useAutoVersion
      ? `v${new Date().toISOString().split('T')[0].replace(/-/g, '.')}.${Date.now() % 1000}`
      : version.trim();

    await onConfirm(finalVersion, message.trim());
  };

  return (
    <>
      {/* Backdrop */}
      <div style={styles.backdrop} onClick={handleClose} />

      {/* Modal */}
      <div style={styles.modal}>
        <div style={styles.header}>
          <h2 style={styles.title}>Publish Catalog</h2>
          <button
            onClick={handleClose}
            style={styles.closeButton}
            disabled={isPublishing}
            aria-label="Close"
          >
            ✕
          </button>
        </div>

        <form onSubmit={handleSubmit}>
          <div style={styles.body}>
            <p style={styles.description}>
              Publishing will commit all pending changes and create a new release tag.
              The storefront will automatically reload with the new catalog.
            </p>

            {/* Version Selection */}
            <div style={styles.section}>
              <label style={styles.sectionLabel}>Version</label>

              <div style={styles.radioGroup}>
                <label style={styles.radioLabel}>
                  <input
                    type="radio"
                    checked={useAutoVersion}
                    onChange={() => setUseAutoVersion(true)}
                    disabled={isPublishing}
                    style={styles.radio}
                  />
                  <span>Auto-generate version (recommended)</span>
                </label>

                <label style={styles.radioLabel}>
                  <input
                    type="radio"
                    checked={!useAutoVersion}
                    onChange={() => setUseAutoVersion(false)}
                    disabled={isPublishing}
                    style={styles.radio}
                  />
                  <span>Specify custom version</span>
                </label>
              </div>

              {!useAutoVersion && (
                <div style={styles.inputGroup}>
                  <input
                    type="text"
                    value={version}
                    onChange={(e) => {
                      setVersion(e.target.value);
                      if (errors.version) {
                        setErrors({ ...errors, version: '' });
                      }
                    }}
                    placeholder="1.0.0"
                    style={{ ...styles.input, ...(errors.version ? styles.inputError : {}) }}
                    disabled={isPublishing}
                  />
                  {errors.version && <span style={styles.errorText}>{errors.version}</span>}
                  <span style={styles.helpText}>
                    Use semantic versioning format (e.g., 1.0.0, 2.1.3)
                  </span>
                </div>
              )}
            </div>

            {/* Release Message */}
            <div style={styles.section}>
              <label htmlFor="message" style={styles.sectionLabel}>
                Release Message <span style={styles.required}>*</span>
              </label>
              <textarea
                id="message"
                value={message}
                onChange={(e) => {
                  setMessage(e.target.value);
                  if (errors.message) {
                    setErrors({ ...errors, message: '' });
                  }
                }}
                placeholder="Describe the changes in this release..."
                style={{ ...styles.textarea, ...(errors.message ? styles.inputError : {}) }}
                disabled={isPublishing}
                rows={4}
              />
              {errors.message && <span style={styles.errorText}>{errors.message}</span>}
              <span style={styles.helpText}>
                Describe what's new or changed in this release (minimum 10 characters)
              </span>
            </div>

            {/* Warning */}
            <div style={styles.warning}>
              <strong>⚠️ Warning:</strong> This action will:
              <ul style={styles.warningList}>
                <li>Commit all pending changes to the git repository</li>
                <li>Push changes to the remote server</li>
                <li>Create a new release tag</li>
                <li>Trigger storefront reload</li>
              </ul>
            </div>
          </div>

          <div style={styles.footer}>
            <button
              type="button"
              onClick={handleClose}
              style={styles.cancelButton}
              disabled={isPublishing}
            >
              Cancel
            </button>
            <button
              type="submit"
              style={styles.confirmButton}
              disabled={isPublishing}
            >
              {isPublishing ? (
                <>
                  <span style={styles.spinner}>⟳</span>
                  Publishing...
                </>
              ) : (
                'Publish Catalog'
              )}
            </button>
          </div>
        </form>
      </div>
    </>
  );
}

const styles = {
  backdrop: {
    position: 'fixed',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
    zIndex: 1000,
  } as React.CSSProperties,
  modal: {
    position: 'fixed',
    top: '50%',
    left: '50%',
    transform: 'translate(-50%, -50%)',
    backgroundColor: 'white',
    borderRadius: '8px',
    boxShadow: '0 20px 25px -5px rgba(0, 0, 0, 0.1), 0 10px 10px -5px rgba(0, 0, 0, 0.04)',
    maxWidth: '600px',
    width: '90%',
    maxHeight: '90vh',
    overflow: 'auto',
    zIndex: 1001,
  } as React.CSSProperties,
  header: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: '1.5rem 2rem',
    borderBottom: '1px solid #e2e8f0',
  } as React.CSSProperties,
  title: {
    margin: 0,
    fontSize: '1.5rem',
    fontWeight: 600,
    color: '#1a202c',
  } as React.CSSProperties,
  closeButton: {
    backgroundColor: 'transparent',
    border: 'none',
    fontSize: '1.5rem',
    color: '#718096',
    cursor: 'pointer',
    padding: '0.25rem',
    lineHeight: 1,
  } as React.CSSProperties,
  body: {
    padding: '2rem',
  } as React.CSSProperties,
  description: {
    margin: '0 0 2rem',
    color: '#4a5568',
    fontSize: '0.875rem',
    lineHeight: 1.5,
  } as React.CSSProperties,
  section: {
    marginBottom: '2rem',
  } as React.CSSProperties,
  sectionLabel: {
    display: 'block',
    marginBottom: '0.75rem',
    fontSize: '0.875rem',
    fontWeight: 600,
    color: '#2d3748',
  } as React.CSSProperties,
  required: {
    color: '#e53e3e',
  } as React.CSSProperties,
  radioGroup: {
    display: 'flex',
    flexDirection: 'column',
    gap: '0.75rem',
    marginBottom: '1rem',
  } as React.CSSProperties,
  radioLabel: {
    display: 'flex',
    alignItems: 'center',
    gap: '0.5rem',
    cursor: 'pointer',
    fontSize: '0.875rem',
    color: '#4a5568',
  } as React.CSSProperties,
  radio: {
    cursor: 'pointer',
  } as React.CSSProperties,
  inputGroup: {
    marginTop: '0.75rem',
  } as React.CSSProperties,
  input: {
    width: '100%',
    padding: '0.75rem',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '1rem',
    transition: 'border-color 0.2s',
  } as React.CSSProperties,
  textarea: {
    width: '100%',
    padding: '0.75rem',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '1rem',
    fontFamily: 'inherit',
    resize: 'vertical',
    transition: 'border-color 0.2s',
  } as React.CSSProperties,
  inputError: {
    borderColor: '#e53e3e',
  } as React.CSSProperties,
  errorText: {
    display: 'block',
    marginTop: '0.25rem',
    fontSize: '0.875rem',
    color: '#e53e3e',
  } as React.CSSProperties,
  helpText: {
    display: 'block',
    marginTop: '0.25rem',
    fontSize: '0.75rem',
    color: '#a0aec0',
  } as React.CSSProperties,
  warning: {
    padding: '1rem',
    backgroundColor: '#fef3cd',
    border: '1px solid #ffeaa7',
    borderRadius: '4px',
    fontSize: '0.875rem',
    color: '#856404',
  } as React.CSSProperties,
  warningList: {
    margin: '0.5rem 0 0',
    paddingLeft: '1.5rem',
  } as React.CSSProperties,
  footer: {
    display: 'flex',
    justifyContent: 'flex-end',
    gap: '1rem',
    padding: '1.5rem 2rem',
    borderTop: '1px solid #e2e8f0',
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
  } as React.CSSProperties,
  confirmButton: {
    display: 'flex',
    alignItems: 'center',
    gap: '0.5rem',
    padding: '0.75rem 1.5rem',
    backgroundColor: '#48bb78',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    fontSize: '1rem',
    fontWeight: 500,
    cursor: 'pointer',
  } as React.CSSProperties,
  spinner: {
    display: 'inline-block',
    animation: 'spin 1s linear infinite',
    fontSize: '1.25rem',
  } as React.CSSProperties,
};
