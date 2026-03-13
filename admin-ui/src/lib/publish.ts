import { Client } from 'urql';

// TODO: Replace with generated types from codegen
interface PublishCatalogInput {
  clientMutationId?: string;
  message: string;
  version?: string | null;
}

interface PublishCatalogPayload {
  clientMutationId?: string | null;
  success: boolean;
  version: string;
  message?: string | null;
}

interface PublishCatalogMutationResponse {
  publishCatalog: PublishCatalogPayload;
}

/**
 * Publish catalog with mutations
 * Commits pending changes, pushes to remote, and creates release tag
 */
export async function publishCatalog(
  client: Client,
  message: string,
  version?: string
): Promise<PublishCatalogPayload> {
  // TODO: Replace with generated mutation from codegen
  // import { PublishCatalogDocument } from '../generated/graphql';

  const PUBLISH_CATALOG_MUTATION = `
    mutation PublishCatalog($input: PublishCatalogInput!) {
      publishCatalog(input: $input) {
        clientMutationId
        success
        version
        message
      }
    }
  `;

  try {
    const result = await client.mutation<PublishCatalogMutationResponse>(
      PUBLISH_CATALOG_MUTATION,
      {
        input: {
          clientMutationId: generateClientMutationId(),
          message,
          version: version || null,
        },
      }
    ).toPromise();

    if (result.error) {
      throw new Error(result.error.message);
    }

    if (!result.data?.publishCatalog) {
      throw new Error('No data returned from publishCatalog mutation');
    }

    return result.data.publishCatalog;
  } catch (error) {
    console.error('Publish catalog failed:', error);
    throw error;
  }
}

/**
 * Check if there are uncommitted changes in the catalog
 * This would typically query git status or check a pending changes flag
 */
export async function hasUncommittedChanges(client: Client): Promise<boolean> {
  // TODO: Implement actual check via GraphQL query
  // This could query git status or check a flag in the backend
  // For now, we'll return a placeholder

  try {
    // Placeholder: In a real implementation, this would:
    // 1. Query backend for git status
    // 2. Check if there are uncommitted files
    // 3. Return true/false based on status

    console.log('Checking for uncommitted changes...');

    // Mock implementation - in production this would be a real query
    return false;
  } catch (error) {
    console.error('Failed to check for uncommitted changes:', error);
    return false;
  }
}

/**
 * Generate a unique client mutation ID for Relay pattern
 */
function generateClientMutationId(): string {
  return `publish-${Date.now()}-${Math.random().toString(36).substring(2, 9)}`;
}

/**
 * Format error message for display to user
 */
export function formatPublishError(error: any): string {
  if (error instanceof Error) {
    return error.message;
  }

  if (typeof error === 'string') {
    return error;
  }

  if (error?.graphQLErrors && error.graphQLErrors.length > 0) {
    return error.graphQLErrors[0].message;
  }

  if (error?.networkError) {
    return 'Network error: Unable to connect to server';
  }

  return 'An unexpected error occurred while publishing';
}
