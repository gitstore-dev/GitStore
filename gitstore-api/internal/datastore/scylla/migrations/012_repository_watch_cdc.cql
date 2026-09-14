-- Repository watch uses the same shared journal as Namespace. CDC is enabled
-- only on the authoritative row; namespace/bucket mappings are projections and
-- must never manufacture independent Repository lifecycle events.
ALTER TABLE repositories_by_uid WITH cdc = {
  'enabled': 'true',
  'preimage': 'full',
  'postimage': 'true',
  'ttl': '1209600'
};
