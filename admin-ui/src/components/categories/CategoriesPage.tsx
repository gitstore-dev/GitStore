import React from 'react';
import { App } from '../App';
import { ProtectedRoute } from '../ProtectedRoute';
import { Header } from '../Header';
import { CategoryList } from './CategoryList';
import { ErrorBoundary } from '../shared/ErrorBoundary';

const styles: Record<string, React.CSSProperties> = {
  main: {
    minHeight: 'calc(100vh - 80px)',
    backgroundColor: '#f7fafc',
  },
  pageHeader: {
    padding: '2rem 2rem 0',
    maxWidth: '1440px',
    margin: '0 auto',
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

export function CategoriesPage() {
  return (
    <App>
      <ProtectedRoute>
        <Header />
        <main style={styles.main}>
          <div style={styles.pageHeader}>
            <h1 style={styles.title}>Categories</h1>
            <p style={styles.subtitle}>Organize your products into hierarchical categories</p>
          </div>
          <ErrorBoundary>
            <CategoryList />
          </ErrorBoundary>
        </main>
      </ProtectedRoute>
    </App>
  );
}
