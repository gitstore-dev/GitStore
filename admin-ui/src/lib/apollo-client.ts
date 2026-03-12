// Apollo Client setup for GraphQL API

import { ApolloClient, InMemoryCache, HttpLink, ApolloLink } from '@apollo/client';
import { v4 as uuidv4 } from 'uuid';
import { logger } from './logger';

// Auth middleware - adds JWT token to requests
const authLink = new ApolloLink((operation, forward) => {
  const token = localStorage.getItem('auth_token');

  operation.setContext(({ headers = {} }) => ({
    headers: {
      ...headers,
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
  }));

  return forward(operation);
});

// Request ID middleware
const requestIdLink = new ApolloLink((operation, forward) => {
  const requestId = uuidv4();
  operation.setContext(({ headers = {} }) => ({
    headers: {
      ...headers,
      'X-Request-ID': requestId,
    },
  }));

  logger.debug('GraphQL request', {
    requestId,
    operationName: operation.operationName,
  });

  return forward(operation);
});

// HTTP link to GraphQL API
const httpLink = new HttpLink({
  uri: import.meta.env.GITSTORE_GRAPHQL_URL || 'http://localhost:4000/graphql',
  credentials: 'same-origin',
});

// Create Apollo Client with optimistic UI support
export const apolloClient = new ApolloClient({
  link: ApolloLink.from([authLink, requestIdLink, httpLink]),
  cache: new InMemoryCache({
    typePolicies: {
      Query: {
        fields: {
          products: {
            keyArgs: false,
            merge(existing, incoming) {
              if (!existing) return incoming;
              return {
                ...incoming,
                edges: [...existing.edges, ...incoming.edges],
              };
            },
          },
          categories: {
            merge(existing, incoming) {
              return incoming;
            },
          },
          collections: {
            merge(existing, incoming) {
              return incoming;
            },
          },
        },
      },
      Product: {
        fields: {
          // Merge function for product updates
          version: {
            merge(_existing, incoming) {
              return incoming;
            },
          },
        },
      },
      Category: {
        fields: {
          children: {
            merge(_existing, incoming) {
              return incoming;
            },
          },
        },
      },
      Collection: {
        fields: {
          productIds: {
            merge(_existing, incoming) {
              return incoming;
            },
          },
        },
      },
    },
  }),
  defaultOptions: {
    watchQuery: {
      fetchPolicy: 'cache-and-network',
      errorPolicy: 'all',
    },
    query: {
      fetchPolicy: 'network-only',
      errorPolicy: 'all',
    },
    mutate: {
      errorPolicy: 'all',
    },
  },
});

// Log Apollo Client initialization
logger.info('Apollo Client initialized', {
  graphqlUrl: import.meta.env.GITSTORE_GRAPHQL_URL || 'http://localhost:4000/graphql',
});
