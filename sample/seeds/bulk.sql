-- Larger dataset for pagination/perf demos. Safe to re-run.
INSERT INTO products (name, price_cents) VALUES
    ('espresso', 300),
    ('americano', 315),
    ('latte', 330),
    ('cappuccino', 345),
    ('mocha', 360),
    ('macchiato', 375),
    ('flat white', 390),
    ('cortado', 405),
    ('drip coffee', 420),
    ('cold brew', 435),
    ('nitro cold brew', 450),
    ('iced latte', 465),
    ('iced mocha', 480),
    ('affogato', 495),
    ('chai latte', 510),
    ('matcha latte', 525),
    ('hot chocolate', 540),
    ('decaf drip', 555),
    ('pour over', 570),
    ('turkish coffee', 585)
ON CONFLICT (name) DO NOTHING;

INSERT INTO users.accounts (email, name) VALUES
    ('ada.lovelace@example.com', 'Ada Lovelace'),
    ('grace.hopper@example.com', 'Grace Hopper'),
    ('alan.turing@example.com', 'Alan Turing'),
    ('katherine.johnson@example.com', 'Katherine Johnson'),
    ('margaret.hamilton@example.com', 'Margaret Hamilton'),
    ('barbara.liskov@example.com', 'Barbara Liskov'),
    ('radia.perlman@example.com', 'Radia Perlman'),
    ('frances.allen@example.com', 'Frances Allen'),
    ('edsger.dijkstra@example.com', 'Edsger Dijkstra'),
    ('donald.knuth@example.com', 'Donald Knuth'),
    ('dennis.ritchie@example.com', 'Dennis Ritchie'),
    ('ken.thompson@example.com', 'Ken Thompson'),
    ('guido.vanrossum@example.com', 'Guido van Rossum'),
    ('james.gosling@example.com', 'James Gosling'),
    ('anders.hejlsberg@example.com', 'Anders Hejlsberg')
ON CONFLICT (email) DO NOTHING;
