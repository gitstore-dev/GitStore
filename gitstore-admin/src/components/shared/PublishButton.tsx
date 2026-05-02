import React, { useState } from 'react';

interface PublishButtonProps {
  onPublish: () => void | Promise<void>;
  disabled?: boolean;
  hasChanges?: boolean;
}

/**
 * Publish button component for catalog publishing
 * Shows pending changes indicator and handles publish action
 */
export function PublishButton({ onPublish, disabled = false, hasChanges = false }: PublishButtonProps) {
  const [isPublishing, setIsPublishing] = useState(false);

  const handleClick = async () => {
    if (disabled || isPublishing) {
      return;
    }

    setIsPublishing(true);
    try {
      await onPublish();
    } catch (error) {
      console.error('Publish failed:', error);
    } finally {
      setIsPublishing(false);
    }
  };

  return (
    <button
      onClick={handleClick}
      disabled={disabled || isPublishing || !hasChanges}
      style={{
        ...styles.button,
        ...(hasChanges && !disabled && !isPublishing ? styles.buttonActive : styles.buttonInactive),
        ...(disabled || isPublishing || !hasChanges ? styles.buttonDisabled : {}),
      }}
      title={
        isPublishing
          ? 'Publishing...'
          : !hasChanges
          ? 'No changes to publish'
          : 'Publish catalog changes'
      }
    >
      {isPublishing ? (
        <>
          <span style={styles.spinner}>⟳</span>
          Publishing...
        </>
      ) : (
        <>
          {hasChanges && <span style={styles.indicator}>●</span>}
          Publish
        </>
      )}
    </button>
  );
}

const styles = {
  button: {
    display: 'flex',
    alignItems: 'center',
    gap: '0.5rem',
    padding: '0.75rem 1.5rem',
    border: 'none',
    borderRadius: '4px',
    fontSize: '1rem',
    fontWeight: 600,
    cursor: 'pointer',
    transition: 'all 0.2s',
    position: 'relative',
  } as React.CSSProperties,
  buttonActive: {
    backgroundColor: '#48bb78',
    color: 'white',
  } as React.CSSProperties,
  buttonInactive: {
    backgroundColor: '#e2e8f0',
    color: '#718096',
  } as React.CSSProperties,
  buttonDisabled: {
    cursor: 'not-allowed',
    opacity: 0.6,
  } as React.CSSProperties,
  indicator: {
    fontSize: '0.75rem',
    color: 'white',
    animation: 'pulse 2s cubic-bezier(0.4, 0, 0.6, 1) infinite',
  } as React.CSSProperties,
  spinner: {
    display: 'inline-block',
    animation: 'spin 1s linear infinite',
    fontSize: '1.25rem',
  } as React.CSSProperties,
};

// Add keyframe animations via style tag
if (typeof document !== 'undefined') {
  const styleSheet = document.createElement('style');
  styleSheet.textContent = `
    @keyframes pulse {
      0%, 100% {
        opacity: 1;
      }
      50% {
        opacity: 0.5;
      }
    }

    @keyframes spin {
      from {
        transform: rotate(0deg);
      }
      to {
        transform: rotate(360deg);
      }
    }
  `;
  if (!document.head.querySelector('style[data-publish-button]')) {
    styleSheet.setAttribute('data-publish-button', 'true');
    document.head.appendChild(styleSheet);
  }
}
