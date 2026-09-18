ALTER TABLE products_by_namespace WITH cdc = {
  'enabled': 'true',
  'preimage': 'full',
  'postimage': 'true',
  'ttl': '1209600'
};
