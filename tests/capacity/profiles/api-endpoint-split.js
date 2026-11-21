// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import http from 'k6/http';
import exec from 'k6/execution';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import { env } from '../lib/config.js';

const admin = env('CAPACITY_ADMIN_URLS').split(',');
const storefront = env('CAPACITY_STOREFRONT_URLS').split(',');
const diagnostic = (__ENV.MODE || 'diagnostic') === 'diagnostic';
const duration = diagnostic ? (__ENV.CAPACITY_DURATION || '10s') : '35m30s';
const unavailable = new Counter('endpoint_public_unavailable');
const adminCompleted = new Counter('endpoint_admin_completed');
const unexpected = new Rate('endpoint_unexpected_errors');
const adminUnexpected = new Rate('endpoint_admin_unexpected_errors');
const publicUnexpected = new Rate('endpoint_public_unexpected_errors');
const unsafePublic = new Counter('endpoint_public_unsafe_responses');
const recoveryAdminLatency = new Trend('endpoint_recovery_admin_duration_ms', true);
const recoveryAdminSuccess = new Rate('endpoint_recovery_admin_success');
const recoveryPublicUnavailable = new Rate('endpoint_recovery_public_unavailable');
const adminLatency = new Trend('endpoint_admin_duration_ms', true);
const headers = {'Content-Type': 'application/json'};
if (__ENV.CAPACITY_TOKEN) headers.Authorization = `Bearer ${__ENV.CAPACITY_TOKEN}`;
for (const [kind, urls] of [['admin', admin], ['storefront', storefront]]) {
  if (urls.some(url => !url.endsWith(`/${kind}/graphql`))) throw new Error(`Expected complete ${kind} URLs`);
}
const publicScenario = diagnostic
  ? {executor: 'constant-arrival-rate', rate: 100, timeUnit: '1s', duration, preAllocatedVUs: 100, maxVUs: 100}
  : {executor: 'ramping-arrival-rate', startRate: 100, timeUnit: '1s', preAllocatedVUs: 100, maxVUs: 100,
      stages: [{target:100,duration:'30m'}, {target:200,duration:'0s'}, {target:200,duration:'5m'}, {target:100,duration:'0s'}, {target:100,duration:'30s'}]};
export const options = {
  scenarios: {
    public_ingress: {...publicScenario, exec:'publicIngress'},
    // Acceptance adds two authoring operations/s and 20 durable observers
    // through the connected driver, giving ten offered Admin operations/s.
    management: {executor:'constant-arrival-rate',exec:'management',rate:diagnostic?10:8,timeUnit:'1s',duration,preAllocatedVUs:20,maxVUs:20},
  },
  thresholds: {checks:['rate>0.999'],dropped_iterations:['count==0'],endpoint_unexpected_errors:['rate<0.001'],endpoint_admin_unexpected_errors:['rate<0.001'],endpoint_public_unexpected_errors:['rate<0.001'],endpoint_public_unsafe_responses:['count==0'],endpoint_admin_duration_ms:['p(95)<=2000'],
    ...(!diagnostic ? {endpoint_recovery_admin_duration_ms:['p(95)<=2000'],endpoint_recovery_admin_success:['rate==1'],endpoint_recovery_public_unavailable:['rate==1']} : {})},
};

function envelope(response) {
  try {
    const value=response.json();
    return value && typeof value==='object' && !Array.isArray(value) ? value : {};
  } catch (_) { return {}; }
}

// Sample the final ten seconds of the thirty-second recovery allowance.
function recoveryWindow() {
  return !diagnostic && Date.now()-exec.scenario.startTime>=2120000;
}

export function publicIngress() {
  const url=storefront[exec.scenario.iterationInTest % storefront.length];
  const response=http.post(url,JSON.stringify({query:'{ __typename }'}),{headers:{'Content-Type':'application/json'},tags:{endpoint:'storefront'},responseCallback:http.expectedStatuses(503,429)});
  const body=envelope(response);
  const error=body.errors?.[0];
  const valid=response.status===503 && Object.keys(body).length===1 && Array.isArray(body.errors) && body.errors.length===1 &&
    error?.message==='Storefront is unavailable' && Object.keys(error).length===2 &&
    error.extensions?.code==='STOREFRONT_UNAVAILABLE' && Object.keys(error.extensions).length===1 &&
    response.headers['Cache-Control']==='no-store';
  unsafePublic.add(body.data!==undefined || (response.status===503 && !valid) ? 1 : 0);
  if (valid) unavailable.add(1);
  const throttled=response.status===429 && body.errors?.[0]?.extensions?.code==='THROTTLED';
  unexpected.add(!valid&&!throttled);
  publicUnexpected.add(!valid&&!throttled);
  if (recoveryWindow()) recoveryPublicUnavailable.add(valid);
  check(response,{'public outcome classified':()=>valid||throttled});
}

export function management() {
  const url=admin[exec.scenario.iterationInTest % admin.length];
  // This probe is a boundary workload. Connected author/status/push/watch
  // correctness requires the companion driver's independently verified receipt.
  const response=http.post(url,JSON.stringify({query:'{ __typename }'}),{headers,tags:{endpoint:'admin'}});
  const body=envelope(response);
  const valid=response.status===200 && body.data?.__typename==='Query' && !body.errors;
  if (valid) adminCompleted.add(1);
  unexpected.add(!valid);adminUnexpected.add(!valid);adminLatency.add(response.timings.duration);
  if (recoveryWindow()) {
    recoveryAdminLatency.add(response.timings.duration);
    recoveryAdminSuccess.add(valid);
  }
  check(response,{'management executed':()=>valid});
}
