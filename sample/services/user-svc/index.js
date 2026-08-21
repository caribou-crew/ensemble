// user-svc is the "brew" sample stack's user accounts service: real CRUD
// over Postgres, fronted by ensemble's proxy like any other service. It
// shares a Postgres instance with catalog-svc but keeps its own tables in
// a dedicated `users` schema rather than the `public` schema catalog-svc
// uses, so the two services' data stays cleanly separated within one
// Postgres container.
'use strict';

const express = require('express');
const { Pool } = require('pg');

const PORT = process.env.PORT || '8082';
const DATABASE_URL = process.env.DATABASE_URL;

if (!DATABASE_URL) {
  console.error('DATABASE_URL is required');
  process.exit(1);
}

const pool = new Pool({ connectionString: DATABASE_URL });

// connectWithRetry polls until Postgres accepts connections. The container
// can take a few seconds to come up after `docker run` returns, and
// ensemble's health gate (which polls /healthz) is what actually blocks
// `ensemble up` — this just means user-svc itself doesn't crash-loop on
// the very first failed connection attempt.
async function connectWithRetry(timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let lastErr;
  while (Date.now() < deadline) {
    try {
      const client = await pool.connect();
      client.release();
      return;
    } catch (err) {
      lastErr = err;
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
  }
  throw new Error(`timed out connecting to database: ${lastErr && lastErr.message}`);
}

async function migrate() {
  await pool.query('CREATE SCHEMA IF NOT EXISTS users');
  await pool.query(`
    CREATE TABLE IF NOT EXISTS users.accounts (
      id         BIGSERIAL PRIMARY KEY,
      email      TEXT NOT NULL UNIQUE,
      name       TEXT NOT NULL,
      created_at TIMESTAMPTZ NOT NULL DEFAULT now()
    )
  `);
}

const app = express();
app.use(express.json());

app.get('/healthz', async (req, res) => {
  try {
    await pool.query('SELECT 1');
    res.sendStatus(200);
  } catch (err) {
    res.status(503).json({ error: 'db unreachable' });
  }
});

app.get('/users', async (req, res) => {
  try {
    const result = await pool.query(
      'SELECT id, email, name, created_at FROM users.accounts ORDER BY id'
    );
    res.status(200).json(result.rows);
  } catch (err) {
    res.status(500).json({ error: err.message });
  }
});

app.get('/users/:id', async (req, res) => {
  try {
    const result = await pool.query(
      'SELECT id, email, name, created_at FROM users.accounts WHERE id = $1',
      [req.params.id]
    );
    if (result.rows.length === 0) {
      res.status(404).json({ error: 'user not found' });
      return;
    }
    res.status(200).json(result.rows[0]);
  } catch (err) {
    res.status(404).json({ error: 'user not found' });
  }
});

app.post('/users', async (req, res) => {
  const { email, name } = req.body || {};
  if (typeof email !== 'string' || !email.trim() || typeof name !== 'string' || !name.trim()) {
    res.status(400).json({ error: 'email and name are required' });
    return;
  }

  try {
    const result = await pool.query(
      `INSERT INTO users.accounts (email, name) VALUES ($1, $2)
       RETURNING id, email, name, created_at`,
      [email, name]
    );
    res.status(201).json(result.rows[0]);
  } catch (err) {
    res.status(400).json({ error: err.message });
  }
});

async function main() {
  await connectWithRetry(30000);
  await migrate();

  app.listen(PORT, () => {
    console.log(`user-svc listening on :${PORT}`);
  });
}

main().catch((err) => {
  console.error(`failed to start user-svc: ${err.message}`);
  process.exit(1);
});
