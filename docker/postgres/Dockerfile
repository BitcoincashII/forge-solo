# The app's database: the official PostgreSQL 16.6 image (Alpine 3.21) plus TimescaleDB 2.17.2,
# and nothing else.
#
# Up to 1.0.12 the app ran timescale/timescaledb:2.17.2-pg16, a 1.8 GB image: about 1.5 GB of it
# is an AI extension (pgai) with its Python stack, LLVM, a pip cache and 13 older TimescaleDB
# versions, none of which this app uses. This image is about 290 MB.
#
# It opens the databases that image created as they are: the same PostgreSQL major version and
# the same C library (Alpine/musl, so text sorts and indexes alike), and the same TimescaleDB
# version, whose library the existing postgresql.conf preloads. Do not change any of the three
# without a migration.
FROM timescale/timescaledb:2.17.2-pg16@sha256:4e459e217f00cbb09920c34d245501e63427e6767a495de57ce76823ff280f12 AS timescale

FROM postgres:16.6-alpine3.21@sha256:1d04b9ba1d4996401f2552b51beda8187f175c0645c091e4781134fc9c9a3eef
COPY --from=timescale /usr/local/lib/postgresql/timescaledb.so /usr/local/lib/postgresql/timescaledb-2.17.2.so /usr/local/lib/postgresql/timescaledb-tsl-2.17.2.so /usr/local/lib/postgresql/
COPY --from=timescale /usr/local/share/postgresql/extension/timescaledb.control /usr/local/share/postgresql/extension/timescaledb--2.17.2.sql /usr/local/share/postgresql/extension/
# A fresh database is set up as the TimescaleDB image does it: the extension, and its tuning.
COPY --from=timescale /usr/local/bin/timescaledb-tune /usr/local/bin/
COPY --from=timescale /docker-entrypoint-initdb.d/ /docker-entrypoint-initdb.d/
# TimescaleDB must be preloaded; a fresh database's postgresql.conf is made from this sample.
RUN sed -r -i "s/[#]*\s*(shared_preload_libraries)\s*=\s*'(.*)'/\1 = 'timescaledb,\2'/;s/,'/'/" /usr/local/share/postgresql/postgresql.conf.sample \
 && grep -q "^shared_preload_libraries = 'timescaledb'" /usr/local/share/postgresql/postgresql.conf.sample

# Sized for this app (a database of a few megabytes on a machine shared with nodes and other apps),
# not for the whole machine. These override postgresql.conf, so they also correct the installs the
# old image's timescaledb-tune sized to all of the machine's memory and cores (15 GB of
# shared_buffers on a 64 GB box, 1 GB on a 4 GB Raspberry Pi). max_locks_per_transaction is left as
# each install has it: clearing an old install's stored shares locks every TimescaleDB chunk at once.
# A fresh database is not tuned at all (NO_TS_TUNE). Telemetry to Timescale is off.
ENV NO_TS_TUNE=1
CMD ["postgres", \
     "-c", "shared_buffers=64MB", \
     "-c", "effective_cache_size=256MB", \
     "-c", "maintenance_work_mem=32MB", \
     "-c", "max_connections=40", \
     "-c", "max_worker_processes=12", \
     "-c", "max_parallel_workers=2", \
     "-c", "max_parallel_workers_per_gather=0", \
     "-c", "timescaledb.max_background_workers=4", \
     "-c", "autovacuum_max_workers=2", \
     "-c", "autovacuum_naptime=60", \
     "-c", "wal_buffers=4MB", \
     "-c", "min_wal_size=80MB", \
     "-c", "max_wal_size=256MB", \
     "-c", "timescaledb.telemetry_level=off"]
