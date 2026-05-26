#!/usr/bin/env node
// Idempotent migration runner. Reads cloud/migrations/*.sql in lexical order
// and applies any not yet recorded in schema_migrations. Each migration is wrapped
// in a single transaction together with the schema_migrations insert.

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import pg from 'pg';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const migDir = path.resolve(__dirname, '..', 'migrations');

const DATABASE_URL = process.env.DATABASE_URL
  || 'postgres://router:router_dev_pw@localhost:55432/router';

const client = new pg.Client({ connectionString: DATABASE_URL });

async function main() {
  await client.connect();

  await client.query(`
    CREATE TABLE IF NOT EXISTS schema_migrations (
      version TEXT PRIMARY KEY,
      applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
    );
  `);

  const { rows: applied } = await client.query('SELECT version FROM schema_migrations');
  const appliedSet = new Set(applied.map(r => r.version));

  const files = fs.readdirSync(migDir).filter(f => f.endsWith('.sql')).sort();

  let count = 0;
  for (const f of files) {
    const version = f.replace(/\.sql$/, '');
    if (appliedSet.has(version)) {
      console.log(`[skip] ${version}`);
      continue;
    }
    const sql = fs.readFileSync(path.join(migDir, f), 'utf8');
    console.log(`[apply] ${version}`);
    try {
      await client.query('BEGIN');
      await client.query(sql);
      await client.query('INSERT INTO schema_migrations(version) VALUES ($1) ON CONFLICT DO NOTHING', [version]);
      await client.query('COMMIT');
      count++;
    } catch (err) {
      await client.query('ROLLBACK').catch(() => {});
      console.error(`[fail] ${version}:`, err.message);
      process.exit(1);
    }
  }

  console.log(`Done. Applied ${count} new migration(s).`);
  await client.end();
}

main().catch(e => { console.error(e); process.exit(1); });
