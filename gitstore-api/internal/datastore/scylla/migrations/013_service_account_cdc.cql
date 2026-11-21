-- Enable full-image publication from the authoritative ServiceAccount table;

ALTER TABLE service_accounts_by_cluster_name WITH cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'};
