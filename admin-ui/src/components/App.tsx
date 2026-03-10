import React, { type ReactNode } from 'react';
import { ApolloProvider } from '@apollo/client';
import { AuthProvider } from '../lib/auth-context';
import { apolloClient } from '../lib/apollo-client';

interface AppProps {
  children: ReactNode;
}

/**
 * Root application component that provides global context providers
 * - AuthProvider: Authentication and session management
 * - ApolloProvider: GraphQL client for data fetching
 */
export function App({ children }: AppProps) {
  return (
    <ApolloProvider client={apolloClient}>
      <AuthProvider>
        {children}
      </AuthProvider>
    </ApolloProvider>
  );
}
