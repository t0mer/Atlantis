-- The SSH port, when the user sets one instead of relying on discovery.
ALTER TABLE networks ADD COLUMN port INTEGER NOT NULL DEFAULT 0;
