-- Forge Solo 1.0.12's PostgreSQL schema, frozen: what its InitDB ran on every start, in order.
-- corePostgresSchema, the migrations after it and the 1175 ledger (Init1175Schema). drift_test.go
-- checks that every column here is carried or left behind for a reason, and that this file is not
-- edited; scripts/it-pg-to-sqlite.sh checks it against 1.0.12's own sources, statement for statement.

DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS timescaledb;
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'TimescaleDB not available, using standard PostgreSQL';
END $$;

CREATE TABLE IF NOT EXISTS blocks (
    id BIGSERIAL PRIMARY KEY,
    height BIGINT NOT NULL UNIQUE,
    hash VARCHAR(64) NOT NULL UNIQUE,
    miner_address VARCHAR(255) NOT NULL,
    reward DECIMAL(20, 8) NOT NULL DEFAULT 50.0,
    difficulty DECIMAL(30, 8) DEFAULT 0,
    status VARCHAR(20) DEFAULT 'confirmed',
    confirmations INT DEFAULT 0,
    is_solo BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    confirmed_at TIMESTAMP WITH TIME ZONE
);

CREATE TABLE IF NOT EXISTS payouts (
    id BIGSERIAL PRIMARY KEY,
    miner_address VARCHAR(255) NOT NULL,
    block_height BIGINT NOT NULL,
    amount DECIMAL(20, 8) NOT NULL,
    confirmed BOOLEAN DEFAULT FALSE,
    txid VARCHAR(128),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    paid_at TIMESTAMP WITH TIME ZONE,
    UNIQUE(miner_address, block_height)
);

CREATE TABLE IF NOT EXISTS miners (
    id BIGSERIAL PRIMARY KEY,
    address VARCHAR(255) UNIQUE NOT NULL,
    solo_mining BOOLEAN DEFAULT FALSE,
    manual_diff DECIMAL(20, 8) DEFAULT 0,
    address_1175 TEXT,
    settings_pin_hash TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS shares (
    id BIGSERIAL,
    time TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    miner_address VARCHAR(255) NOT NULL,
    worker_name VARCHAR(255) NOT NULL,
    job_id VARCHAR(64),
    difficulty DECIMAL(20, 8) NOT NULL,
    is_valid BOOLEAN NOT NULL DEFAULT TRUE,
    is_block BOOLEAN DEFAULT FALSE,
    is_solo BOOLEAN DEFAULT FALSE,
    block_hash VARCHAR(64),
    PRIMARY KEY (id, time)
);

DO $$
BEGIN
    PERFORM create_hypertable('shares', 'time', chunk_time_interval => INTERVAL '1 hour', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'Could not create hypertable for shares';
END $$;

CREATE TABLE IF NOT EXISTS pool_stats (
    time TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW() PRIMARY KEY,
    hashrate DECIMAL(30, 2) NOT NULL DEFAULT 0,
    workers INT NOT NULL DEFAULT 0,
    miners_online INT NOT NULL DEFAULT 0,
    valid_shares BIGINT NOT NULL DEFAULT 0,
    invalid_shares BIGINT NOT NULL DEFAULT 0,
    network_difficulty DECIMAL(30, 8) DEFAULT 0,
    block_height BIGINT DEFAULT 0
);

CREATE TABLE IF NOT EXISTS pool_config (
    id INT PRIMARY KEY DEFAULT 1,
    pool_address TEXT DEFAULT '',
    payout_address_1175 TEXT DEFAULT '',
    coinbase_tag TEXT DEFAULT '',
    payout_mode TEXT DEFAULT 'solo',
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS datum_identity (
    id INT PRIMARY KEY CHECK (id = 1),
    key_seed TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_blocks_height ON blocks(height);
CREATE INDEX IF NOT EXISTS idx_blocks_miner ON blocks(miner_address);
CREATE INDEX IF NOT EXISTS idx_payouts_miner ON payouts(miner_address);
CREATE INDEX IF NOT EXISTS idx_payouts_unpaid ON payouts(miner_address) WHERE txid IS NULL OR txid = '';
CREATE INDEX IF NOT EXISTS idx_payouts_block ON payouts(block_height);
CREATE INDEX IF NOT EXISTS idx_shares_miner_time ON shares(miner_address, time DESC);
CREATE INDEX IF NOT EXISTS idx_miners_address ON miners(address);

ALTER TABLE blocks ADD COLUMN IF NOT EXISTS is_solo BOOLEAN DEFAULT FALSE;
ALTER TABLE miners ADD COLUMN IF NOT EXISTS address_1175 TEXT;
ALTER TABLE miners ADD COLUMN IF NOT EXISTS settings_pin_hash TEXT;
ALTER TABLE payouts ADD COLUMN IF NOT EXISTS status VARCHAR(20) DEFAULT 'pending';
ALTER TABLE pool_config ADD COLUMN IF NOT EXISTS payout_mode TEXT DEFAULT 'solo';
ALTER TABLE miners DROP COLUMN IF EXISTS min_payout;
ALTER TABLE pool_config DROP COLUMN IF EXISTS min_payout;

CREATE TABLE IF NOT EXISTS blocks_1175 (
    height        BIGINT PRIMARY KEY,
    hash          TEXT NOT NULL,
    gross_reward  DOUBLE PRECISION NOT NULL,
    is_solo       BOOLEAN DEFAULT false,
    finder        TEXT,
    distributed   BOOLEAN DEFAULT false,
    status        TEXT DEFAULT 'pending',
    created_at    TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS payouts_1175 (
    id           BIGSERIAL PRIMARY KEY,
    miner_address TEXT NOT NULL,
    block_height BIGINT NOT NULL,
    amount       DOUBLE PRECISION NOT NULL,
    txid         TEXT,
    status       TEXT DEFAULT 'pending',
    batch        TEXT,
    paid_at      TIMESTAMPTZ,
    created_at   TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(miner_address, block_height)
);

CREATE INDEX IF NOT EXISTS idx_payouts_1175_pending ON payouts_1175 (miner_address) WHERE status = 'pending';

ALTER TABLE blocks_1175 ADD COLUMN IF NOT EXISTS is_solo BOOLEAN DEFAULT false;
ALTER TABLE blocks_1175 ADD COLUMN IF NOT EXISTS finder TEXT;
ALTER TABLE blocks_1175 ADD COLUMN IF NOT EXISTS distributed BOOLEAN DEFAULT false;
ALTER TABLE payouts_1175 ADD COLUMN IF NOT EXISTS batch TEXT;
