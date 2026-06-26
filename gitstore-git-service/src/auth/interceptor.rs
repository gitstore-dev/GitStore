// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::sync::Arc;

use subtle::ConstantTimeEq;
use tonic::{service::Interceptor, Request, Status};

/// Validates `Authorization: Bearer <token>` on every inbound gRPC call.
/// Accepts calls that carry the primary secret or, during a rotation window, the previous secret.
#[derive(Clone)]
pub struct HmacInterceptor {
    secret: Arc<str>,
    secret_previous: Option<Arc<str>>,
}

impl HmacInterceptor {
    pub fn new(secret: &str, previous: Option<&str>) -> Self {
        Self {
            secret: Arc::from(secret),
            secret_previous: previous.filter(|s| !s.is_empty()).map(Arc::from),
        }
    }
}

impl Interceptor for HmacInterceptor {
    fn call(&mut self, req: Request<()>) -> Result<Request<()>, Status> {
        let token = req
            .metadata()
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.strip_prefix("Bearer "));

        let ct_eq = |a: &str, b: &str| -> bool { a.as_bytes().ct_eq(b.as_bytes()).into() };

        match token {
            None => Err(Status::unauthenticated("missing inter-service token")),
            Some(t) if ct_eq(t, self.secret.as_ref()) => Ok(req),
            Some(t) => {
                if let Some(prev) = &self.secret_previous {
                    if ct_eq(t, prev.as_ref()) {
                        return Ok(req);
                    }
                }
                Err(Status::unauthenticated("invalid inter-service token"))
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn req_with_bearer(token: &str) -> Request<()> {
        let mut req = Request::new(());
        let value = format!("Bearer {}", token).parse().unwrap();
        req.metadata_mut().insert("authorization", value);
        req
    }

    // T019: interceptor rejects call with no Authorization header
    #[test]
    fn test_hmac_interceptor_rejects_missing_header() {
        let mut interceptor = HmacInterceptor::new("secret", None);
        let req = Request::new(());
        let result = interceptor.call(req);
        assert!(result.is_err());
        let status = result.unwrap_err();
        assert_eq!(status.code(), tonic::Code::Unauthenticated);
        assert!(status.message().contains("missing"));
    }

    // T020: interceptor rejects call with wrong token
    #[test]
    fn test_hmac_interceptor_rejects_wrong_token() {
        let mut interceptor = HmacInterceptor::new("secret", None);
        let req = req_with_bearer("wrong");
        let result = interceptor.call(req);
        assert!(result.is_err());
        let status = result.unwrap_err();
        assert_eq!(status.code(), tonic::Code::Unauthenticated);
        assert!(status.message().contains("invalid"));
    }

    // T021: interceptor accepts call with correct token
    #[test]
    fn test_hmac_interceptor_accepts_correct_token() {
        let mut interceptor = HmacInterceptor::new("secret", None);
        let req = req_with_bearer("secret");
        let result = interceptor.call(req);
        assert!(result.is_ok());
    }

    // T032: interceptor accepts old token during rotation window
    #[test]
    fn test_hmac_interceptor_accepts_previous_token() {
        let mut interceptor = HmacInterceptor::new("new-secret", Some("old-secret"));
        let req = req_with_bearer("old-secret");
        let result = interceptor.call(req);
        assert!(result.is_ok());
    }

    // T033: interceptor rejects old token after rotation window is closed
    #[test]
    fn test_hmac_interceptor_rejects_old_token_after_window_closed() {
        let mut interceptor = HmacInterceptor::new("new-secret", None);
        let req = req_with_bearer("old-secret");
        let result = interceptor.call(req);
        assert!(result.is_err());
        let status = result.unwrap_err();
        assert_eq!(status.code(), tonic::Code::Unauthenticated);
    }

    // Empty previous secret must be treated as absent — not as a valid rotation token.
    #[test]
    fn test_hmac_interceptor_empty_previous_does_not_authenticate_empty_bearer() {
        let mut interceptor = HmacInterceptor::new("secret", Some(""));
        // Authorization: Bearer  (empty token after the space)
        let req = req_with_bearer("");
        let result = interceptor.call(req);
        assert!(result.is_err(), "empty bearer must not authenticate");
        let status = result.unwrap_err();
        assert_eq!(status.code(), tonic::Code::Unauthenticated);
    }
}
