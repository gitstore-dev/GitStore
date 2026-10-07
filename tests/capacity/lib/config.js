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
  const value = Number.parseInt(__ENV[name] || `${fallback}`, 10);
  if (!Number.isInteger(value) || value < minimum) {
    throw new Error(`${name} must be an integer >= ${minimum}`);
  }
  return value;
}

export { env, integer };
