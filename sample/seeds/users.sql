-- user-svc creates its own schema/table on startup; this only inserts
-- starter rows and is safe to re-run.
INSERT INTO users.accounts (email, name) VALUES
    ('ada@example.com', 'Ada Lovelace'),
    ('grace@example.com', 'Grace Hopper')
ON CONFLICT (email) DO NOTHING;
