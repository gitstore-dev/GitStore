import React, { type ReactNode, useMemo } from 'react';
import { Provider as UrqlProvider } from 'urql';
import { AuthProvider } from '../lib/auth-context';
import { getUrqlClient } from '../lib/urql-client';

interface AppProps {
  children: ReactNode;
}

/**
 * Root application component that provides global context providers
 * - AuthProvider: Authentication and session management
 * - UrqlProvider: GraphQL client for data fetching
 */
export function App({ children }: Readonly<AppProps>) {
  // Create client only once when component mounts (in browser)
  const urqlClient = useMemo(() => getUrqlClient(), []);

  return (
    <UrqlProvider value={urqlClient}>
      <AuthProvider>
        {children}
      </AuthProvider>
    </UrqlProvider>
  );
}
