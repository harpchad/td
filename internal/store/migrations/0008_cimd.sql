-- Client ID Metadata Documents. 0004 already described the two ways a client
-- row arrives and reserved 'cimd' for this one; these are the columns that
-- make a resolved document a cache rather than a registration.
--
-- A CIMD client is not registered, it is resolved. The row exists so the
-- foreign keys from oauth_code and oauth_grant hold, and so the settings page
-- can list and revoke the grant like any other. It is refetched when it goes
-- stale rather than trusted forever.
ALTER TABLE oauth_client ADD COLUMN metadata_fetched_at TEXT;
ALTER TABLE oauth_client ADD COLUMN metadata_expires_at TEXT;
