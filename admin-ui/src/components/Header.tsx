import React, { useState } from 'react';
import { useAuth } from '../lib/auth-context';
import { PublishButton } from './shared/PublishButton';

/**
 * Application header with navigation and user menu
 */
export function Header() {
  const { user, logout } = useAuth();
  const [hasChanges, setHasChanges] = useState(false);

  const handleLogout = async () => {
    try {
      await logout();
      window.location.href = '/login';
    } catch (error) {
      console.error('Logout failed:', error);
    }
  };

  const handlePublish = async () => {
    // This will be implemented in T123
    console.log('Publishing catalog...');
    // TODO: Open publish modal with version input and confirmation
  };

  return (
    <header style={styles.header}>
      <div style={styles.container}>
        <div style={styles.brand}>
          <h1 style={styles.title}>GitStore Admin</h1>
        </div>

        <nav style={styles.nav}>
          <a href="/products" style={styles.navLink}>
            Products
          </a>
          <a href="/categories" style={styles.navLink}>
            Categories
          </a>
          <a href="/collections" style={styles.navLink}>
            Collections
          </a>
        </nav>

        <div style={styles.actions}>
          <PublishButton onPublish={handlePublish} hasChanges={hasChanges} />
        </div>

        <div style={styles.userMenu}>
          {user && (
            <>
              <span style={styles.username}>{user.username}</span>
              <button onClick={handleLogout} style={styles.logoutBtn}>
                Logout
              </button>
            </>
          )}
        </div>
      </div>
    </header>
  );
}

const styles = {
  header: {
    backgroundColor: 'white',
    borderBottom: '1px solid #e2e8f0',
    padding: '0',
  } as React.CSSProperties,
  container: {
    maxWidth: '1440px',
    margin: '0 auto',
    padding: '1rem 2rem',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    gap: '2rem',
  } as React.CSSProperties,
  brand: {
    display: 'flex',
    alignItems: 'center',
  } as React.CSSProperties,
  title: {
    margin: 0,
    fontSize: '1.5rem',
    fontWeight: 600,
    color: '#1a202c',
  } as React.CSSProperties,
  nav: {
    display: 'flex',
    gap: '2rem',
    flex: 1,
  } as React.CSSProperties,
  navLink: {
    color: '#4a5568',
    textDecoration: 'none',
    fontSize: '1rem',
    fontWeight: 500,
    transition: 'color 0.2s',
  } as React.CSSProperties,
  actions: {
    display: 'flex',
    alignItems: 'center',
  } as React.CSSProperties,
  userMenu: {
    display: 'flex',
    alignItems: 'center',
    gap: '1rem',
  } as React.CSSProperties,
  username: {
    color: '#4a5568',
    fontSize: '0.875rem',
    fontWeight: 500,
  } as React.CSSProperties,
  logoutBtn: {
    padding: '0.5rem 1rem',
    backgroundColor: 'transparent',
    color: '#e53e3e',
    border: '1px solid #e53e3e',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    cursor: 'pointer',
    transition: 'all 0.2s',
  } as React.CSSProperties,
};
