ALTER TABLE repositories_by_uid WITH cdc = {
  'enabled': 'true',
  'preimage': 'full',
  'postimage': 'true',
  'ttl': '1209600'
};
