// Health check endpoint for admin UI
import type { APIRoute } from 'astro';

const startTime = Date.now();
const version = '1.0.0';

export const GET: APIRoute = async () => {
  return new Response(
    JSON.stringify({
      status: 'healthy',
      version,
      timestamp: new Date().toISOString(),
    }),
    {
      status: 200,
      headers: {
        'Content-Type': 'application/json',
      },
    }
  );
};
