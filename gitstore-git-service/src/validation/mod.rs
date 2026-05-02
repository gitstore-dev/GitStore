// Validation module for catalog entities

pub mod category;
pub mod collection;
pub mod errors;
pub mod product;
pub mod validator;

// Re-export main types for convenience
pub use validator::Validator;
