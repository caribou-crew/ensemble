-- catalog-svc creates its own schema on startup (CREATE TABLE IF NOT
-- EXISTS); this seed only inserts starter rows, and is safe to re-run.
INSERT INTO products (name, price_cents) VALUES
    ('espresso', 350),
    ('drip coffee', 275),
    ('cold brew', 425)
ON CONFLICT (name) DO NOTHING;
