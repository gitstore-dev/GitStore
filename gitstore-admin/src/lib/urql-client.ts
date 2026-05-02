// urql Client setup for GraphQL API

import { createClient, fetchExchange, cacheExchange, type Client } from 'urql';
import { v4 as uuidv4 } from 'uuid';
import { logger } from './logger';

let clientInstance: Client | null = null;

// Create urql client lazily (only in browser)
export function getUrqlClient(): Client {
  if (clientInstance) {
    return clientInstance;
  }

  // Only create client in browser environment
  if (typeof window === 'undefined') {
    throw new TypeError('urql client can only be created in browser environment');
  }

  clientInstance = createClient({
    url: import.meta.env.GITSTORE_GRAPHQL_URL || 'http://localhost:4000/graphql',
    exchanges: [
      cacheExchange,
      fetchExchange,
    ],
    fetchOptions: () => {
      const token = localStorage.getItem('auth_token');
      const requestId = uuidv4();

      logger.debug('GraphQL request', { requestId });

      return {
        credentials: 'same-origin',
        headers: {
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
          'X-Request-ID': requestId,
        },
      };
    },
  });

  // Log urql client initialization
  logger.info('urql client initialized', {
    graphqlUrl: import.meta.env.GITSTORE_GRAPHQL_URL || 'http://localhost:4000/graphql',
  });

  return clientInstance;
}
