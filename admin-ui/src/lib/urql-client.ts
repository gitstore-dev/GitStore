// urql Client setup for GraphQL API

import { createClient, fetchExchange, cacheExchange } from 'urql';
import { v4 as uuidv4 } from 'uuid';
import { logger } from './logger';

// Create urql client with auth and request ID exchanges
export const urqlClient = createClient({
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
