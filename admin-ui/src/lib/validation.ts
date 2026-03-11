/**
 * Client-side validation library
 * Validates required fields, formats, and constraints before mutation submission
 * Provides immediate feedback to catch errors early
 */

export interface ValidationError {
  field: string;
  message: string;
}

export interface ValidationResult {
  isValid: boolean;
  errors: ValidationError[];
}

/**
 * Base validator class
 */
abstract class Validator<T> {
  protected errors: ValidationError[] = [];

  abstract validate(data: T): ValidationResult;

  protected addError(field: string, message: string): void {
    this.errors.push({ field, message });
  }

  protected reset(): void {
    this.errors = [];
  }

  protected getResult(): ValidationResult {
    return {
      isValid: this.errors.length === 0,
      errors: this.errors,
    };
  }
}

/**
 * Product validation
 */
interface ProductInput {
  title?: string;
  slug?: string;
  sku?: string | null;
  price?: number;
  inventory?: number;
  status?: string;
  description?: string | null;
  categoryIds?: string[];
  images?: string[];
}

export class ProductValidator extends Validator<ProductInput> {
  validate(data: ProductInput): ValidationResult {
    this.reset();

    // Title validation
    if (data.title !== undefined) {
      if (!data.title || !data.title.trim()) {
        this.addError('title', 'Title is required');
      } else if (data.title.trim().length < 3) {
        this.addError('title', 'Title must be at least 3 characters');
      } else if (data.title.trim().length > 200) {
        this.addError('title', 'Title must not exceed 200 characters');
      }
    }

    // Slug validation
    if (data.slug !== undefined) {
      if (!data.slug || !data.slug.trim()) {
        this.addError('slug', 'Slug is required');
      } else if (!/^[a-z0-9-]+$/.test(data.slug)) {
        this.addError('slug', 'Slug can only contain lowercase letters, numbers, and hyphens');
      } else if (data.slug.length < 3) {
        this.addError('slug', 'Slug must be at least 3 characters');
      } else if (data.slug.length > 100) {
        this.addError('slug', 'Slug must not exceed 100 characters');
      }
    }

    // SKU validation
    if (data.sku !== undefined && data.sku !== null && data.sku.trim()) {
      if (data.sku.length > 50) {
        this.addError('sku', 'SKU must not exceed 50 characters');
      }
      if (!/^[A-Z0-9-]+$/.test(data.sku)) {
        this.addError('sku', 'SKU can only contain uppercase letters, numbers, and hyphens');
      }
    }

    // Price validation
    if (data.price !== undefined) {
      if (typeof data.price !== 'number') {
        this.addError('price', 'Price must be a number');
      } else if (data.price < 0) {
        this.addError('price', 'Price cannot be negative');
      } else if (data.price > 999999.99) {
        this.addError('price', 'Price cannot exceed 999,999.99');
      } else if (!Number.isFinite(data.price)) {
        this.addError('price', 'Price must be a valid number');
      }
    }

    // Inventory validation
    if (data.inventory !== undefined) {
      if (typeof data.inventory !== 'number') {
        this.addError('inventory', 'Inventory must be a number');
      } else if (data.inventory < 0) {
        this.addError('inventory', 'Inventory cannot be negative');
      } else if (!Number.isInteger(data.inventory)) {
        this.addError('inventory', 'Inventory must be a whole number');
      } else if (data.inventory > 999999) {
        this.addError('inventory', 'Inventory cannot exceed 999,999');
      }
    }

    // Status validation
    if (data.status !== undefined) {
      const validStatuses = ['draft', 'published', 'archived'];
      if (!validStatuses.includes(data.status)) {
        this.addError('status', `Status must be one of: ${validStatuses.join(', ')}`);
      }
    }

    // Description validation
    if (data.description !== undefined && data.description !== null && data.description.trim()) {
      if (data.description.length > 5000) {
        this.addError('description', 'Description must not exceed 5000 characters');
      }
    }

    // Category IDs validation
    if (data.categoryIds !== undefined) {
      if (!Array.isArray(data.categoryIds)) {
        this.addError('categoryIds', 'Category IDs must be an array');
      } else if (data.categoryIds.length > 10) {
        this.addError('categoryIds', 'Product can have at most 10 categories');
      }
    }

    // Images validation
    if (data.images !== undefined) {
      if (!Array.isArray(data.images)) {
        this.addError('images', 'Images must be an array');
      } else if (data.images.length > 20) {
        this.addError('images', 'Product can have at most 20 images');
      } else {
        data.images.forEach((image, index) => {
          if (typeof image !== 'string') {
            this.addError(`images[${index}]`, 'Image URL must be a string');
          } else if (!this.isValidUrl(image)) {
            this.addError(`images[${index}]`, 'Image URL must be a valid URL');
          }
        });
      }
    }

    return this.getResult();
  }

  private isValidUrl(url: string): boolean {
    try {
      new URL(url);
      return true;
    } catch {
      return false;
    }
  }
}

/**
 * Category validation
 */
interface CategoryInput {
  name?: string;
  slug?: string;
  description?: string | null;
  parentId?: string | null;
  displayOrder?: number;
}

export class CategoryValidator extends Validator<CategoryInput> {
  validate(data: CategoryInput): ValidationResult {
    this.reset();

    // Name validation
    if (data.name !== undefined) {
      if (!data.name || !data.name.trim()) {
        this.addError('name', 'Name is required');
      } else if (data.name.trim().length < 2) {
        this.addError('name', 'Name must be at least 2 characters');
      } else if (data.name.trim().length > 100) {
        this.addError('name', 'Name must not exceed 100 characters');
      }
    }

    // Slug validation
    if (data.slug !== undefined) {
      if (!data.slug || !data.slug.trim()) {
        this.addError('slug', 'Slug is required');
      } else if (!/^[a-z0-9-]+$/.test(data.slug)) {
        this.addError('slug', 'Slug can only contain lowercase letters, numbers, and hyphens');
      } else if (data.slug.length < 2) {
        this.addError('slug', 'Slug must be at least 2 characters');
      } else if (data.slug.length > 100) {
        this.addError('slug', 'Slug must not exceed 100 characters');
      }
    }

    // Description validation
    if (data.description !== undefined && data.description !== null && data.description.trim()) {
      if (data.description.length > 2000) {
        this.addError('description', 'Description must not exceed 2000 characters');
      }
    }

    // Display order validation
    if (data.displayOrder !== undefined) {
      if (typeof data.displayOrder !== 'number') {
        this.addError('displayOrder', 'Display order must be a number');
      } else if (data.displayOrder < 0) {
        this.addError('displayOrder', 'Display order cannot be negative');
      } else if (!Number.isInteger(data.displayOrder)) {
        this.addError('displayOrder', 'Display order must be a whole number');
      } else if (data.displayOrder > 9999) {
        this.addError('displayOrder', 'Display order cannot exceed 9999');
      }
    }

    return this.getResult();
  }
}

/**
 * Collection validation
 */
interface CollectionInput {
  name?: string;
  slug?: string;
  description?: string | null;
  productIds?: string[];
  displayOrder?: number;
}

export class CollectionValidator extends Validator<CollectionInput> {
  validate(data: CollectionInput): ValidationResult {
    this.reset();

    // Name validation
    if (data.name !== undefined) {
      if (!data.name || !data.name.trim()) {
        this.addError('name', 'Name is required');
      } else if (data.name.trim().length < 2) {
        this.addError('name', 'Name must be at least 2 characters');
      } else if (data.name.trim().length > 100) {
        this.addError('name', 'Name must not exceed 100 characters');
      }
    }

    // Slug validation
    if (data.slug !== undefined) {
      if (!data.slug || !data.slug.trim()) {
        this.addError('slug', 'Slug is required');
      } else if (!/^[a-z0-9-]+$/.test(data.slug)) {
        this.addError('slug', 'Slug can only contain lowercase letters, numbers, and hyphens');
      } else if (data.slug.length < 2) {
        this.addError('slug', 'Slug must be at least 2 characters');
      } else if (data.slug.length > 100) {
        this.addError('slug', 'Slug must not exceed 100 characters');
      }
    }

    // Description validation
    if (data.description !== undefined && data.description !== null && data.description.trim()) {
      if (data.description.length > 2000) {
        this.addError('description', 'Description must not exceed 2000 characters');
      }
    }

    // Product IDs validation
    if (data.productIds !== undefined) {
      if (!Array.isArray(data.productIds)) {
        this.addError('productIds', 'Product IDs must be an array');
      } else if (data.productIds.length > 100) {
        this.addError('productIds', 'Collection can have at most 100 products');
      }
    }

    // Display order validation
    if (data.displayOrder !== undefined) {
      if (typeof data.displayOrder !== 'number') {
        this.addError('displayOrder', 'Display order must be a number');
      } else if (data.displayOrder < 0) {
        this.addError('displayOrder', 'Display order cannot be negative');
      } else if (!Number.isInteger(data.displayOrder)) {
        this.addError('displayOrder', 'Display order must be a whole number');
      } else if (data.displayOrder > 9999) {
        this.addError('displayOrder', 'Display order cannot exceed 9999');
      }
    }

    return this.getResult();
  }
}

/**
 * Publish catalog validation
 */
interface PublishInput {
  message?: string;
  version?: string | null;
}

export class PublishValidator extends Validator<PublishInput> {
  validate(data: PublishInput): ValidationResult {
    this.reset();

    // Message validation
    if (data.message !== undefined) {
      if (!data.message || !data.message.trim()) {
        this.addError('message', 'Release message is required');
      } else if (data.message.trim().length < 10) {
        this.addError('message', 'Release message must be at least 10 characters');
      } else if (data.message.trim().length > 500) {
        this.addError('message', 'Release message must not exceed 500 characters');
      }
    }

    // Version validation (if provided)
    if (data.version !== undefined && data.version !== null && data.version.trim()) {
      const semverRegex = /^v?\d+\.\d+\.\d+$/;
      if (!semverRegex.test(data.version.trim())) {
        this.addError('version', 'Version must be in semantic versioning format (e.g., 1.0.0 or v1.0.0)');
      }
    }

    return this.getResult();
  }
}

/**
 * Helper function to validate product data
 */
export function validateProduct(data: ProductInput): ValidationResult {
  const validator = new ProductValidator();
  return validator.validate(data);
}

/**
 * Helper function to validate category data
 */
export function validateCategory(data: CategoryInput): ValidationResult {
  const validator = new CategoryValidator();
  return validator.validate(data);
}

/**
 * Helper function to validate collection data
 */
export function validateCollection(data: CollectionInput): ValidationResult {
  const validator = new CollectionValidator();
  return validator.validate(data);
}

/**
 * Helper function to validate publish data
 */
export function validatePublish(data: PublishInput): ValidationResult {
  const validator = new PublishValidator();
  return validator.validate(data);
}

/**
 * Format validation errors for display
 */
export function formatValidationErrors(errors: ValidationError[]): string {
  if (errors.length === 0) {
    return '';
  }

  if (errors.length === 1) {
    return errors[0].message;
  }

  return errors.map(err => `• ${err.message}`).join('\n');
}

/**
 * Get first error for a specific field
 */
export function getFieldError(errors: ValidationError[], fieldName: string): string | null {
  const error = errors.find(err => err.field === fieldName);
  return error ? error.message : null;
}

/**
 * Check if a field has errors
 */
export function hasFieldError(errors: ValidationError[], fieldName: string): boolean {
  return errors.some(err => err.field === fieldName);
}
