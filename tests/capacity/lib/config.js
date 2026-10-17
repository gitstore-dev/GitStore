// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

function env(name, fallback = '') {
  const value = __ENV[name] || fallback;
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function integer(name, fallback, minimum = 1) {
  const raw = __ENV[name] || `${fallback}`;
  const value = /^\d+$/.test(raw) ? Number(raw) : NaN;
  if (!Number.isSafeInteger(value) || value < minimum) {
    throw new Error(`${name} must be an exact decimal integer >= ${minimum}`);
  }
  return value;
}

function durationNanoseconds(duration) {
  if (typeof duration !== 'string') throw new Error('Capacity scenario has an invalid normalized native duration');
  const parts = [...duration.matchAll(/(\d+)(?:\.(\d+))?(h|m|s)/g)];
  if (parts.length === 0 || parts.map((part) => part[0]).join('') !== duration) {
    throw new Error('Capacity scenario has an invalid normalized native duration');
  }
  const units = { h: BigInt('3600000000000'), m: BigInt('60000000000'), s: BigInt('1000000000') };
  let result = BigInt(0);
  for (const part of parts) {
    result += BigInt(part[1]) * units[part[3]];
    if (part[2]) result += BigInt(part[2]) * units[part[3]] / (BigInt(10) ** BigInt(part[2].length));
  }
  if (result <= BigInt(0)) throw new Error('Capacity scenario duration must be positive');
  return result;
}

function announceLoadWindow(start, durationNs) {
  if (!__ENV.CAPACITY_CHAOS_PROFILE) return;
  const duration = BigInt(durationNs);
  const end = start + Number(duration / BigInt('1000000'));
  const now = Date.now();
  if (!__ENV.CAPACITY_RUN_ID || !Number.isSafeInteger(start) || start <= 0 ||
      duration <= BigInt(0) || !Number.isSafeInteger(end) || end <= start ||
      now < start || now >= end) {
    throw new Error('Capacity chaos requires an active exact declared load window');
  }
  console.info(`CAPACITY_LOAD_WINDOW ${JSON.stringify({
    schemaVersion: 1, runId: __ENV.CAPACITY_RUN_ID, startMs: start, endMs: end,
  })}`);
}

export { env, integer, durationNanoseconds, announceLoadWindow };
