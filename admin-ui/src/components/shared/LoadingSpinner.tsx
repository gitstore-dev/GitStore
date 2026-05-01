import React, { useEffect } from 'react';

// Inject spin keyframe once
function useSpinKeyframe() {
  useEffect(() => {
    const id = 'gitstore-spin-keyframe';
    if (!document.getElementById(id)) {
      const style = document.createElement('style');
      style.id = id;
      style.textContent = '@keyframes spin { to { transform: rotate(360deg); } }';
      document.head.appendChild(style);
    }
  }, []);
}

interface LoadingSpinnerProps {
  message?: string;
  size?: 'sm' | 'md' | 'lg';
  fullPage?: boolean;
}

export function LoadingSpinner({
  message = 'Loading...',
  size = 'md',
  fullPage = false,
}: LoadingSpinnerProps) {
  useSpinKeyframe();
  const spinnerSize = { sm: 24, md: 40, lg: 56 }[size];

  const content = (
    <div style={styles.wrapper}>
      <svg
        width={spinnerSize}
        height={spinnerSize}
        viewBox="0 0 24 24"
        fill="none"
        style={styles.spinner}
        aria-hidden="true"
      >
        <circle cx="12" cy="12" r="10" stroke="#e2e8f0" strokeWidth="3" />
        <path
          d="M12 2a10 10 0 0 1 10 10"
          stroke="#667eea"
          strokeWidth="3"
          strokeLinecap="round"
        />
      </svg>
      {message && <span style={styles.message}>{message}</span>}
    </div>
  );

  if (fullPage) {
    return <div style={styles.fullPage}>{content}</div>;
  }

  return content;
}

const styles: Record<string, React.CSSProperties> = {
  fullPage: {
    display: 'flex',
    justifyContent: 'center',
    alignItems: 'center',
    minHeight: '40vh',
    padding: '4rem',
  },
  wrapper: {
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    gap: '0.75rem',
  },
  spinner: {
    animation: 'spin 0.8s linear infinite',
  },
  message: {
    color: '#718096',
    fontSize: '0.9375rem',
  },
};
