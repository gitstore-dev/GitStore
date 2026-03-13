import React, { useState, useEffect } from 'react';
import { useAuth } from '../lib/auth-context';
import { PublishButton } from './shared/PublishButton';
import { PublishModal } from './shared/PublishModal';
import { useClient } from 'urql';
import { publishCatalog, hasUncommittedChanges, formatPublishError } from '../lib/publish';

/**
 * Application header with navigation and user menu
 */
export function Header() {
  const { user, logout } = useAuth();
  const client = useClient();
  const [hasChanges, setHasChanges] = useState(false);
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [isPublishing, setIsPublishing] = useState(false);
  const [publishError, setPublishError] = useState<string | null>(null);

  // Check for uncommitted changes periodically
  useEffect(() => {
    const checkChanges = async () => {
      try {
        const changes = await hasUncommittedChanges(client);
        setHasChanges(changes);
      } catch (error) {
        console.error('Failed to check for changes:', error);
      }
    };

    // Check immediately
    checkChanges();

    // Check every 30 seconds
    const interval = setInterval(checkChanges, 30000);

    return () => clearInterval(interval);
  }, [client]);

  const handleLogout = async () => {
    try {
      await logout();
      window.location.href = '/login';
    } catch (error) {
      console.error('Logout failed:', error);
    }
  };

  const handlePublishClick = () => {
    setPublishError(null);
    setIsModalOpen(true);
  };

  const handlePublishConfirm = async (version: string, message: string) => {
    setIsPublishing(true);
    setPublishError(null);

    try {
      const result = await publishCatalog(client, message, version || undefined);

      if (result.success) {
        console.log('Catalog published successfully:', result.version);
        setIsModalOpen(false);
        setHasChanges(false);

        // Show success message
        alert(`Catalog published successfully!\nVersion: ${result.version}`);

        // Optionally reload the page to reflect changes
        // window.location.reload();
      } else {
        throw new Error(result.message || 'Publish failed');
      }
    } catch (error) {
      console.error('Publish failed:', error);
      const errorMessage = formatPublishError(error);
      setPublishError(errorMessage);
      alert(`Publish failed: ${errorMessage}`);
    } finally {
      setIsPublishing(false);
    }
  };

  return (
    <>
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
            <PublishButton onPublish={handlePublishClick} hasChanges={hasChanges} />
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

      <PublishModal
        isOpen={isModalOpen}
        onClose={() => setIsModalOpen(false)}
        onConfirm={handlePublishConfirm}
        isPublishing={isPublishing}
      />
    </>
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
