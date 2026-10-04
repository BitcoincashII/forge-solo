-- Forge Solo 1.0.12's data as a home install holds it, for testing the move to SQLite on every
-- platform: loaded with psql into a database 1.0.12 made (or one made with
-- internal/pgmigrate/testdata/pg-1.0.12-schema.sql). The same file gives the same rows everywhere:
-- every time is fixed, written in another zone, most with a fraction of a second.
--
-- Times the dashboard shows carry fractions below half a second, so they read the same before and
-- after the move, which rounds to the second; times it does not show carry a half or more, so a
-- move that cuts instead of rounding shows in the figures.
--
-- The settings rows are inserted only when 1.0.12's own code has not written them already.

SET client_encoding = 'UTF8';
SET TIME ZONE 'America/New_York';

-- TIDES mode, a 1175 payout address and a tag beyond ASCII.
INSERT INTO pool_config (id, pool_address, payout_address_1175, coinbase_tag, payout_mode, updated_at)
VALUES (1, 'bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2',
        'esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x', '/forge ü 1.0.12/', 'tides',
        '2026-09-20 10:00:00.700001')
ON CONFLICT (id) DO NOTHING;

-- The TIDES key, which names this install at Forge Pool (its Gateway ID).
INSERT INTO datum_identity (id, key_seed, created_at)
VALUES (1, repeat('c3', 32), '2026-09-10 08:00:00.5')
ON CONFLICT (id) DO NOTHING;

-- Miner A: solo, its 1175 address and a settings PIN. Miner C: a manual difficulty, nothing else.
INSERT INTO miners (address, solo_mining, manual_diff, address_1175, settings_pin_hash, created_at, updated_at) VALUES
 ('bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2', true, 0,
  'esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x',
  '$2a$10$abcdefghijklmnopqrstuuJ0123456789abcdefghijklmnopqrstu',
  '2026-08-01 09:00:00.5', '2026-09-19 18:30:00.999999'),
 ('bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4', false, 1234.5, NULL, NULL,
  '2026-08-02 09:00:00.75', '2026-08-02 09:00:00.75')
ON CONFLICT (address) DO UPDATE SET settings_pin_hash = EXCLUDED.settings_pin_hash;

-- 150 solo blocks of miner A, ten minutes apart: confirmed below 100120, orphaned at 100141 and
-- 100142, pending above. A third of the rewards carry fees.
INSERT INTO blocks (height, hash, miner_address, reward, status, is_solo, created_at, confirmed_at)
SELECT 100000 + g, md5('a' || g) || md5('b' || g), 'bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2',
       CASE WHEN g % 3 = 1 THEN 3.12512345 ELSE 3.125 END,
       CASE WHEN g IN (141, 142) THEN 'orphaned' WHEN g < 120 THEN 'confirmed' ELSE 'pending' END, true,
       timestamptz '2026-09-15 12:00:00.25' + make_interval(mins => g * 10),
       CASE WHEN g < 120 THEN timestamptz '2026-09-15 13:40:00.5' + make_interval(mins => g * 10) END
FROM generate_series(0, 149) g;

-- 100150: found again at its height by a later block, which replaced the first (1.0.12 keeps only
-- the later one, with the later found time).
INSERT INTO blocks (height, hash, miner_address, reward, status, is_solo, created_at)
VALUES (100150, md5('superseding') || md5('100150'), 'bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2',
        3.125, 'pending', true, '2026-09-16 13:20:00.125');

-- Each solo block pays miner A in its own coinbase; an orphaned one is voided.
INSERT INTO payouts (miner_address, block_height, amount, confirmed, txid, status, created_at, paid_at)
SELECT miner_address, height, reward, status <> 'orphaned',
       CASE WHEN status = 'orphaned' THEN 'orphaned' ELSE 'coinbase-direct' END,
       CASE WHEN status = 'orphaned' THEN 'orphaned' ELSE 'paid' END,
       created_at + interval '0.125 second', created_at
FROM blocks WHERE is_solo;

-- An older pool-style block split three ways: paid, pending without a txid, pending with an empty
-- one; and a credit at a height with no block, with every optional column empty.
INSERT INTO blocks (height, hash, miner_address, reward, status, is_solo, created_at, confirmed_at)
VALUES (99000, md5('d') || md5('dd'), 'bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4', 50, 'confirmed', false,
        '2026-07-20 10:00:00.375', '2026-07-20 11:00:00.625');
INSERT INTO payouts (miner_address, block_height, amount, confirmed, txid, status, created_at, paid_at) VALUES
 ('bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4', 99000, 33.33333333, true, repeat('ab', 32), 'paid',
  '2026-07-20 10:00:00.375', '2026-07-21 10:00:00.125'),
 ('bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2', 99000, 16.66666667, false, NULL, 'pending',
  '2026-07-20 10:00:00.375', NULL),
 ('bitcoincashii:qzet9v4jk2et9v4jk2et9v4jk2et9v4jkg0xty3z5s', 99000, 0.00000001, false, '', 'pending',
  '2026-07-20 10:00:00.375', NULL),
 ('bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4', 99100, 0.00000001, NULL, NULL, NULL,
  '2026-07-30 10:00:00.375', NULL);

-- 1175 blocks: settled, orphaned, found but not yet distributed, and a PPLNS one split two ways.
INSERT INTO blocks_1175 (height, hash, gross_reward, is_solo, finder, distributed, status, created_at) VALUES
 (5000, md5('x5000'), 0.78125, true, 'bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2', true, 'confirmed',
  '2026-09-12 10:00:00.25'),
 (5001, md5('x5001'), 0.78125, true, 'bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2', true, 'orphaned',
  '2026-09-13 10:00:00.25'),
 (5002, md5('x5002'), 0.78125, true, 'bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2', false, 'pending',
  '2026-09-21 10:00:00.25'),
 (5010, md5('x5010'), 0.7, false, 'bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4', true, 'confirmed',
  '2026-09-02 10:00:00.25');
INSERT INTO payouts_1175 (miner_address, block_height, amount, txid, status, batch, paid_at, created_at) VALUES
 ('bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2', 5000, 0.78125, 'coinbase-direct', 'paid', NULL,
  '2026-09-14 10:00:00.25', '2026-09-12 10:00:00.5'),
 ('bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2', 5001, 0.78125, 'orphaned', 'orphaned', NULL, NULL,
  '2026-09-13 10:00:00.5'),
 ('bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4', 5010, 0.525, NULL, 'pending', 'b-7', NULL,
  '2026-09-02 10:00:00.5'),
 ('bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2', 5010, 0.175, NULL, 'pending', NULL, NULL,
  '2026-09-02 10:00:00.5');

-- 1,100 hours of stored shares: on TimescaleDB, one chunk per hour. The move never reads them.
INSERT INTO shares (time, miner_address, worker_name, difficulty, is_solo)
SELECT timestamptz '2026-08-01 00:00:00+00' + make_interval(hours => g),
       'bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2', 'rig1', 1024, true
FROM generate_series(1, 1100) g;
