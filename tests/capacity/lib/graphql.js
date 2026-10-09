// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import http from 'k6/http';

export function graphql(url, token, query, variables, tags = {}) {
  const response = http.post(url, JSON.stringify({ query, variables }), {
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
    tags,
  });
  let body;
  try {
    body = response.json();
  } catch (_) {
    body = { errors: [{ message: 'non-JSON GraphQL response' }] };
  }
  return { response, body };
}
