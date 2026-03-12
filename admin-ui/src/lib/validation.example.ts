/**
 * Example usage of validation library
 * This file demonstrates how to use validators in components
 */

import {
  validateProduct,
  validateCategory,
  validateCollection,
  validatePublish,
  formatValidationErrors,
  getFieldError,
  hasFieldError,
} from './validation';

/**
 * Example 1: Product form validation
 */
function exampleProductValidation() {
  const productData = {
    title: 'New Product',
    slug: 'new-product',
    sku: 'NP-001',
    price: 99.99,
    inventory: 10,
    status: 'draft',
  };

  const result = validateProduct(productData);

  if (!result.isValid) {
    console.error('Product validation failed:');
    console.error(formatValidationErrors(result.errors));
    return false;
  }

  console.log('Product validation passed!');
  return true;
}

/**
 * Example 2: Category form validation with error display
 */
function exampleCategoryValidation() {
  const categoryData = {
    name: 'Electronics',
    slug: 'electronics',
    description: 'Electronic devices and accessories',
    displayOrder: 1,
  };

  const result = validateCategory(categoryData);

  if (!result.isValid) {
    // Show errors for each field
    if (hasFieldError(result.errors, 'name')) {
      const error = getFieldError(result.errors, 'name');
      console.error('Name error:', error);
    }

    if (hasFieldError(result.errors, 'slug')) {
      const error = getFieldError(result.errors, 'slug');
      console.error('Slug error:', error);
    }

    return false;
  }

  console.log('Category validation passed!');
  return true;
}

/**
 * Example 3: Collection form validation in React component
 */
function exampleReactComponentValidation() {
  // In a React component:
  /*
  const [formData, setFormData] = useState({
    name: '',
    slug: '',
    productIds: [],
    displayOrder: 0,
  });
  const [errors, setErrors] = useState<ValidationError[]>([]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    // Validate before submitting
    const result = validateCollection(formData);

    if (!result.isValid) {
      setErrors(result.errors);
      return;
    }

    // Clear errors
    setErrors([]);

    // Submit to API
    try {
      await createCollectionMutation({ variables: { input: formData } });
    } catch (error) {
      console.error('Mutation failed:', error);
    }
  };

  // In render:
  <input
    type="text"
    value={formData.name}
    onChange={(e) => setFormData({ ...formData, name: e.target.value })}
    className={hasFieldError(errors, 'name') ? 'error' : ''}
  />
  {hasFieldError(errors, 'name') && (
    <span className="error-message">{getFieldError(errors, 'name')}</span>
  )}
  */
}

/**
 * Example 4: Real-time validation on field change
 */
function exampleRealtimeValidation() {
  // In a React component:
  /*
  const handleFieldChange = (field: string, value: any) => {
    // Update form data
    const newData = { ...formData, [field]: value };
    setFormData(newData);

    // Validate just this field
    const result = validateProduct(newData);

    // Update errors for this field only
    if (hasFieldError(result.errors, field)) {
      setErrors((prev) => [
        ...prev.filter(err => err.field !== field),
        ...result.errors.filter(err => err.field === field),
      ]);
    } else {
      // Clear errors for this field
      setErrors((prev) => prev.filter(err => err.field !== field));
    }
  };
  */
}

/**
 * Example 5: Publish validation
 */
function examplePublishValidation() {
  const publishData = {
    message: 'Release v1.0.0 with new features',
    version: '1.0.0',
  };

  const result = validatePublish(publishData);

  if (!result.isValid) {
    alert('Validation failed:\n' + formatValidationErrors(result.errors));
    return false;
  }

  console.log('Publish validation passed!');
  return true;
}

/**
 * Example 6: Validation before mutation
 */
async function exampleValidationBeforeMutation() {
  const productData = {
    title: 'Gaming Laptop',
    slug: 'gaming-laptop',
    sku: 'GL-001',
    price: 1499.99,
    inventory: 5,
    status: 'published',
  };

  // Validate first
  const validationResult = validateProduct(productData);

  if (!validationResult.isValid) {
    console.error('Cannot submit: validation failed');
    console.error(formatValidationErrors(validationResult.errors));
    return;
  }

  // Only submit if validation passes
  try {
    // await createProductMutation({ variables: { input: productData } });
    console.log('Product created successfully!');
  } catch (error) {
    console.error('Mutation failed:', error);
  }
}

// Export examples for demonstration
export {
  exampleProductValidation,
  exampleCategoryValidation,
  exampleReactComponentValidation,
  exampleRealtimeValidation,
  examplePublishValidation,
  exampleValidationBeforeMutation,
};
