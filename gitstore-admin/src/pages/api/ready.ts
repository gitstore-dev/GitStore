// Readiness check endpoint for admin UI
import type { APIRoute } from 'astro';

const startTime = Date.now();
const version = '1.0.0';

interface CheckStatus {
  status: string;
  message: string;
}

interface ReadinessResponse {
  status: string;
  version: string;
  timestamp: string;
  checks: {
    uptime: CheckStatus;
    api_connectivity: CheckStatus;
  };
}

async function checkAPIConnectivity(): Promise<CheckStatus> {
  const apiUrl = import.meta.env.PUBLIC_API_URL || 'http://localhost:4000';

  try {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 3000);

    const response = await fetch(`${apiUrl}/health`, {
      signal: controller.signal,
    });

    clearTimeout(timeout);

    if (response.ok) {
      return {
        status: 'healthy',
        message: 'API accessible',
      };
    } else {
      return {
        status: 'degraded',
        message: `API returned status ${response.status}`,
      };
    }
  } catch (error) {
    return {
      status: 'unhealthy',
      message: 'API unreachable',
    };
  }
}

function checkUptime(): CheckStatus {
  const uptimeSeconds = Math.floor((Date.now() - startTime) / 1000);

  if (uptimeSeconds < 5) {
    return {
      status: 'degraded',
      message: 'service warming up',
    };
  }

  return {
    status: 'healthy',
    message: 'service operational',
  };
}

export const GET: APIRoute = async () => {
  const [uptimeCheck, apiCheck] = await Promise.all([
    Promise.resolve(checkUptime()),
    checkAPIConnectivity(),
  ]);

  // Determine overall status
  let overallStatus = 'healthy';
  let httpStatus = 200;

  if (apiCheck.status === 'unhealthy') {
    overallStatus = 'unhealthy';
    httpStatus = 503;
  } else if (uptimeCheck.status === 'degraded' || apiCheck.status === 'degraded') {
    overallStatus = 'degraded';
  }

  const response: ReadinessResponse = {
    status: overallStatus,
    version,
    timestamp: new Date().toISOString(),
    checks: {
      uptime: uptimeCheck,
      api_connectivity: apiCheck,
    },
  };

  return new Response(JSON.stringify(response), {
    status: httpStatus,
    headers: {
      'Content-Type': 'application/json',
    },
  });
};
