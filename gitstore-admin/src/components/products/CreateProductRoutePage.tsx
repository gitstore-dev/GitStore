import React from 'react';
import { App } from '../App';
import { ProtectedRoute } from '../ProtectedRoute';
import { Header } from '../Header';
import { CreateProductPage } from './CreateProductPage';

const styles: Record<string, React.CSSProperties> = {
  main: {
    minHeight: 'calc(100vh - 80px)',
    backgroundColor: '#f7fafc',
    paddingBottom: '2rem',
  },
  pageHeader: {
    padding: '2rem 2rem 0',
    maxWidth: '1440px',
    margin: '0 auto',
  },
  breadcrumbs: {
    marginBottom: '1rem',
    fontSize: '0.875rem',
    color: '#718096',
  },
  breadcrumbLink: {
    color: '#667eea',
    textDecoration: 'none',
  },
  separator: {
    margin: '0 0.5rem',
    color: '#cbd5e0',
  },
  current: {
    color: '#4a5568',
    fontWeight: 500,
  },
  title: {
    margin: 0,
    fontSize: '2rem',
    fontWeight: 700,
    color: '#1a202c',
  },
  subtitle: {
    margin: '0.5rem 0 0',
    color: '#718096',
    fontSize: '1rem',
  },
};

export function CreateProductRoutePage() {
  return (
    <App>
      <ProtectedRoute>
        <Header />
        <main style={styles.main}>
          <div style={styles.pageHeader}>
            <div style={styles.breadcrumbs}>
              <a href="/products" style={styles.breadcrumbLink}>Products</a>
              <span style={styles.separator}>/</span>
              <span style={styles.current}>New Product</span>
            </div>
            <h1 style={styles.title}>Create Product</h1>
            <p style={styles.subtitle}>Add a new product to your catalog</p>
          </div>
          <CreateProductPage />
        </main>
      </ProtectedRoute>
    </App>
  );
}
