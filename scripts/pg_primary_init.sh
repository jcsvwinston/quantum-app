#!/bin/bash
# Runs inside the postgres:16 primary container on first init
# (docker-entrypoint-initdb.d): creates the replication role and allows
# remote replication connections so the standby can pg_basebackup + stream.
set -e
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD 'replicator'"
echo "host replication replicator all scram-sha-256" >> "$PGDATA/pg_hba.conf"
