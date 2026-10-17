// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import http from 'k6/http';

function graphqlResponseValid(result) {
  return !!(result && result.response && Number.isInteger(result.response.status) &&
    result.response.status >= 200 && result.response.status < 300 &&
    result.body && typeof result.body === 'object' && !Array.isArray(result.body) &&
    (result.body.data === undefined || result.body.data === null ||
      (typeof result.body.data === 'object' && !Array.isArray(result.body.data))));
}

export function graphqlSucceeded(result) {
  return !!(graphqlResponseValid(result) &&
    result.body.data && typeof result.body.data === 'object' && !Array.isArray(result.body.data) &&
    (result.body.errors === undefined || (Array.isArray(result.body.errors) && result.body.errors.length === 0)));
}

export function graphqlErrorCode(result) {
  if (!graphqlResponseValid(result) || !Array.isArray(result.body.errors) || result.body.errors.length === 0) {
    return null;
  }
  const first = result.body.errors[0];
  const code = first && first.extensions && first.extensions.code;
  return typeof code === 'string' && code.length > 0 &&
    result.body.errors.every((error) => error && error.extensions && error.extensions.code === code) ? code : null;
}

function graphqlRequest(url, token, query, variables, tags, params) {
  return { method: 'POST', url, body: JSON.stringify({ query, variables }), params: {
    ...params,
    headers: {
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      'Content-Type': 'application/json',
    },
    tags,
  } };
}

function graphqlResponse(response) {
  let body;
  try {
    body = response.json();
  } catch (_) {
    body = { errors: [{ message: 'non-JSON GraphQL response' }] };
  }
  return { response, body };
}

export function graphql(url, token, query, variables, tags = {}, params = {}) {
  const request = graphqlRequest(url, token, query, variables, tags, params);
  return graphqlResponse(http.post(request.url, request.body, request.params));
}

export function graphqlPair(urls, token, query, variables, tags = {}, params = {}) {
  if (!Array.isArray(urls) || urls.length !== 2 || urls[0] === urls[1] ||
      !urls.every((url) => typeof url === 'string' && url.length > 0)) {
    throw new Error('GraphQL read pair requires two distinct endpoints');
  }
  const responses = http.batch(urls.map((url) => graphqlRequest(url, token, query, variables, tags, params)));
  if (!Array.isArray(responses) || responses.length !== 2) {
    throw new Error('GraphQL read pair did not return both responses');
  }
  return responses.map(graphqlResponse);
}
