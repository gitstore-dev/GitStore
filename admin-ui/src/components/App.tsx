import React, { type ReactNode } from 'react';
import { Provider as UrqlProvider } from 'urql';
import { AuthProvider } from '../lib/auth-context';
import { urqlClient } from '../lib/urql-client';

interface AppProps {
  children: ReactNode;
}

/**
 * Root application component that provides global context providers
 * - AuthProvider: Authentication and session management
 * - UrqlProvider: GraphQL client for data fetching
 */
export function App({ children }: AppProps) {
  return (
    <UrqlProvider value={urqlClient}>
      <AuthProvider>
        {children}
      </AuthProvider>
    </UrqlProvider>
  );
}
