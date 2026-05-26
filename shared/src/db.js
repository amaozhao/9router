// Postgres connection pool — one shared pool per process.
// Exposes query() and a tx() helper that runs a callback inside a transaction.

import pg from 'pg';
import { config } from './config.js';
import { logger } from './logger.js';

const { Pool } = pg;

let _pool = null;

export function getPool() {
  if (!_pool) {
    _pool = new Pool({
      connectionString: config.databaseUrl,
      max: 20,
      idleTimeoutMillis: 30_000,
      connectionTimeoutMillis: 5_000,
    });
    _pool.on('error', (err) => logger.error({ err }, 'pg pool error'));
  }
  return _pool;
}

/** Run a single parameterized query. */
export async function query(text, params) {
  const pool = getPool();
  return pool.query(text, params);
}

/** Run a callback inside a transaction. Auto-commits on success, rolls back on throw. */
export async function tx(fn) {
  const pool = getPool();
  const client = await pool.connect();
  try {
    await client.query('BEGIN');
    const result = await fn(client);
    await client.query('COMMIT');
    return result;
  } catch (err) {
    await client.query('ROLLBACK').catch(() => {});
    throw err;
  } finally {
    client.release();
  }
}

/** Close the pool (for tests and graceful shutdown). */
export async function closeDb() {
  if (_pool) {
    await _pool.end();
    _pool = null;
  }
}
