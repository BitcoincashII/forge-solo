# The app's database: the official PostgreSQL 16.15 image (Alpine 3.23) plus TimescaleDB 2.17.2,
# and nothing else.
#
# Up to 1.0.12 the app ran timescale/timescaledb:2.17.2-pg16, a 1.8 GB image: about 1.5 GB of it
# is an AI extension (pgai) with its Python stack, LLVM, a pip cache and 13 older TimescaleDB
# versions, none of which this app uses. This image is about 300 MB.
#
# It opens the databases that image created as they are: the same PostgreSQL major version and
# the same C library (Alpine/musl, so text sorts and indexes alike), and the same TimescaleDB
# version, whose library the existing postgresql.conf preloads. Do not change any of the three
# without a migration.
#
# PostgreSQL itself is 16.15, not the old image's 16.6: a minor release, which keeps the data
# directory, with the 45 fixes since. TimescaleDB stays 2.17.2, run the way Timescale's own
# timescaledb:2.30.2-pg16 image (PostgreSQL 16.15) runs databases still at 2.17.2: that image's
# loader (timescaledb.so, which PostgreSQL preloads and which loads the version a database has) and
# its 2.17.2 libraries, byte for byte the ones the old image shipped -- checked below. Moving
# TimescaleDB on is an ALTER EXTENSION, for a release of its own.
FROM timescale/timescaledb:2.17.2-pg16@sha256:4e459e217f00cbb09920c34d245501e63427e6767a495de57ce76823ff280f12 AS timescale
FROM timescale/timescaledb:2.30.2-pg16@sha256:6f139d56042989bd35f50ba5986e492cd32e35cc6778c50be2659add298dc09f AS loader

FROM postgres:16.15-alpine3.23@sha256:621a761097839bdb50207afd6b87a72f38e2d718dd46c3d744828d8917c4f1e0
COPY --from=loader /usr/local/lib/postgresql/timescaledb.so /usr/local/lib/postgresql/timescaledb-2.17.2.so /usr/local/lib/postgresql/timescaledb-tsl-2.17.2.so /usr/local/lib/postgresql/
# The 2.17.2 libraries are the old image's, byte for byte, on each platform the app ships for.
RUN case "$(uname -m)" in \
      x86_64)  printf '%s  %s\n' \
                 532081dc6f27da5f75f67042c772faa73f09dbe251d6ad7c67091ee0aaf4fecc timescaledb-2.17.2.so \
                 f32945073abbe8a8b337ecdc169a9aa08bbe0fffa5c409f033273d2f216ebfb2 timescaledb-tsl-2.17.2.so ;; \
      aarch64) printf '%s  %s\n' \
                 3374233e9d4e66e62e96554b532c4fad7ff9e5ecdfa0d8bbcc9f78361973d539 timescaledb-2.17.2.so \
                 5796e28fb480725e8b92c94b1fd3de213abca6ea0b55fe429537a1729cd9ae5f timescaledb-tsl-2.17.2.so ;; \
      *) echo "no checked TimescaleDB 2.17.2 libraries for $(uname -m)"; exit 1 ;; \
    esac > /tmp/ts.sha256 \
 && cd /usr/local/lib/postgresql && sha256sum -c /tmp/ts.sha256 && rm /tmp/ts.sha256
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
