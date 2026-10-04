-- A database Forge Solo 1.0.0 made on Umbrel, for scripts/it-pg-to-sqlite.sh (case 8): its compose's
-- TimescaleDB image with its init-db.sql, then its own InitDB, then rows in the columns it had. The app's
-- tables only, as pg_dump wrote them; the stored shares, which the move never reads, are left out.
--
-- PostgreSQL database dump
--

-- Dumped from database version 16.6
-- Dumped by pg_dump version 16.6

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: blocks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.blocks (
    id bigint NOT NULL,
    height bigint NOT NULL,
    hash character varying(64) NOT NULL,
    miner_address character varying(255) NOT NULL,
    reward numeric(20,8) DEFAULT 50.0 NOT NULL,
    difficulty numeric(30,8) DEFAULT 0,
    status character varying(20) DEFAULT 'confirmed'::character varying,
    confirmations integer DEFAULT 0,
    is_solo boolean DEFAULT false,
    created_at timestamp with time zone DEFAULT now(),
    confirmed_at timestamp with time zone
);


--
-- Name: blocks_1175; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.blocks_1175 (
    height bigint NOT NULL,
    hash text NOT NULL,
    gross_reward double precision NOT NULL,
    is_solo boolean DEFAULT false,
    finder text,
    distributed boolean DEFAULT false,
    status text DEFAULT 'pending'::text,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: blocks_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.blocks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: blocks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.blocks_id_seq OWNED BY public.blocks.id;


--
-- Name: miners; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.miners (
    id bigint NOT NULL,
    address character varying(255) NOT NULL,
    solo_mining boolean DEFAULT false,
    manual_diff numeric(20,8) DEFAULT 0,
    min_payout numeric(20,8) DEFAULT 5.0,
    address_1175 text,
    settings_pin_hash text,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: miners_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.miners_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: miners_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.miners_id_seq OWNED BY public.miners.id;


--
-- Name: payouts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payouts (
    id bigint NOT NULL,
    miner_address character varying(255) NOT NULL,
    block_height bigint NOT NULL,
    amount numeric(20,8) NOT NULL,
    confirmed boolean DEFAULT false,
    txid character varying(128),
    created_at timestamp with time zone DEFAULT now(),
    paid_at timestamp with time zone
);


--
-- Name: payouts_1175; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payouts_1175 (
    id bigint NOT NULL,
    miner_address text NOT NULL,
    block_height bigint NOT NULL,
    amount double precision NOT NULL,
    txid text,
    status text DEFAULT 'pending'::text,
    batch text,
    paid_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: payouts_1175_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.payouts_1175_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: payouts_1175_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.payouts_1175_id_seq OWNED BY public.payouts_1175.id;


--
-- Name: payouts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.payouts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: payouts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.payouts_id_seq OWNED BY public.payouts.id;


--
-- Name: shares_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.shares_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: blocks id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blocks ALTER COLUMN id SET DEFAULT nextval('public.blocks_id_seq'::regclass);


--
-- Name: miners id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.miners ALTER COLUMN id SET DEFAULT nextval('public.miners_id_seq'::regclass);


--
-- Name: payouts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts ALTER COLUMN id SET DEFAULT nextval('public.payouts_id_seq'::regclass);


--
-- Name: payouts_1175 id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts_1175 ALTER COLUMN id SET DEFAULT nextval('public.payouts_1175_id_seq'::regclass);


--
-- Data for Name: blocks; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.blocks (id, height, hash, miner_address, reward, difficulty, status, confirmations, is_solo, created_at, confirmed_at) FROM stdin;
1	80000	dbde90434954003eb005492f1f6be69bc6f39f097abe76fdf479e9ee4ee64884	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00000000	1234567.50000000	confirmed	100	f	2026-03-05 11:00:00.4+00	2026-03-06 11:00:00.6+00
2	80001	bbfaba9aaf84ebabcea9b91f422aa9e1cd435012a9aedd98b2a0ced9b1db22ed	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00012345	1234568.50000000	confirmed	100	t	2026-03-05 11:10:00.4+00	2026-03-06 11:10:00.6+00
3	80002	ba3b2f30743d469ba3ad706e7d33df84b5e012ce4df4ab3c9e22944ef7a8875e	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00000000	1234569.50000000	confirmed	100	t	2026-03-05 11:20:00.4+00	2026-03-06 11:20:00.6+00
4	80003	dda92045c774b60c9256d4af47570405bce46a866bc950513a9ef6aed64ce01b	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00012345	1234570.50000000	confirmed	100	f	2026-03-05 11:30:00.4+00	2026-03-06 11:30:00.6+00
5	80004	c0765d7957cb74a3c70d4dd086dfa5ab3de44f3d9979731bdc479c6a370e160a	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00000000	1234571.50000000	confirmed	100	t	2026-03-05 11:40:00.4+00	2026-03-06 11:40:00.6+00
6	80005	8a86ad7022476faa1b793c1dfa244b937a104a7780d124fa1168c8b6c929a6de	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00012345	1234572.50000000	confirmed	100	t	2026-03-05 11:50:00.4+00	2026-03-06 11:50:00.6+00
7	80006	59a25121b3de1fa7248e2465920b5e52a8efc994422bceed19ab615c1c7de012	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00000000	1234573.50000000	confirmed	100	f	2026-03-05 12:00:00.4+00	2026-03-06 12:00:00.6+00
8	80007	0da2f64a033de9e898ef556e270d66d49e73e6bfaa035426c47579f947b988c6	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00012345	1234574.50000000	orphaned	100	t	2026-03-05 12:10:00.4+00	2026-03-06 12:10:00.6+00
9	80008	9df57cd3fdf19ca12000f213cfad99ada2a530f6019826baa02c573ac4c1cb2b	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00000000	1234575.50000000	confirmed	100	t	2026-03-05 12:20:00.4+00	2026-03-06 12:20:00.6+00
10	80009	235413c62fb550b476e4ac3440e17950166430d3e96d22b142e32c3c82692fa2	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00012345	1234576.50000000	confirmed	100	f	2026-03-05 12:30:00.4+00	2026-03-06 12:30:00.6+00
11	80010	78d5b276db5d75e21959395a6914bf1e8d7a41419c2c83533d803e4bdb520b82	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00000000	1234577.50000000	confirmed	100	t	2026-03-05 12:40:00.4+00	2026-03-06 12:40:00.6+00
12	80011	53d9770c582858413c3e9d7b452b68ecf193081764f8faff0c5130fae13c7eb8	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00012345	1234578.50000000	confirmed	100	t	2026-03-05 12:50:00.4+00	2026-03-06 12:50:00.6+00
13	80012	ffe626e0615b4a539686e1ae0d74e046d1933521cc9c90183281d06987c21c1a	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00000000	1234579.50000000	confirmed	100	f	2026-03-05 13:00:00.4+00	2026-03-06 13:00:00.6+00
14	80013	172d4aa2394af091c69c12cde6f99db135f00dcbeae4cb40a3bc3c5da236d4b5	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00012345	1234580.50000000	confirmed	100	t	2026-03-05 13:10:00.4+00	2026-03-06 13:10:00.6+00
15	80014	060e4397f1768fa8801833bc6ab05a3cd1084ac803a57e3376e4e766466494f4	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00000000	1234581.50000000	confirmed	100	t	2026-03-05 13:20:00.4+00	2026-03-06 13:20:00.6+00
16	80015	948e72b020543fabe3b5478778f9585dc289b39c80d7d0485bd7360112449785	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00012345	1234582.50000000	pending	3	f	2026-03-05 13:30:00.4+00	\N
17	80016	e84a5dd52df001dc27791406bd2c2c8a7f5533f21f75fcdc7415bedaf4b9872a	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00000000	1234583.50000000	pending	3	t	2026-03-05 13:40:00.4+00	\N
18	80017	b5707970c385e08201ec95064f97a827b928302400c2ead5989e9a73f8cad1cb	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00012345	1234584.50000000	pending	3	t	2026-03-05 13:50:00.4+00	\N
19	80018	1973b429db7168b66e5040705817d9f9da660b08a582aa666656f4ad37c1f1f6	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00000000	1234585.50000000	pending	3	f	2026-03-05 14:00:00.4+00	\N
20	80019	2e3f6b54061acfcf72660446745d85e5d9d6a99a6d13e6c6bb17a4a6ffb45d84	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	50.00012345	1234586.50000000	pending	3	t	2026-03-05 14:10:00.4+00	\N
\.


--
-- Data for Name: blocks_1175; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.blocks_1175 (height, hash, gross_reward, is_solo, finder, distributed, status, created_at) FROM stdin;
3000	178ea2200aa50e440f3f5964c3aa6b70	0.78125	t	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	t	confirmed	2026-03-07 09:00:00.5+00
3001	250bb684928a481d0b1af323f832cc4b	0.78125	t	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	f	pending	2026-03-08 09:00:00.25+00
\.


--
-- Data for Name: miners; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.miners (id, address, solo_mining, manual_diff, min_payout, address_1175, settings_pin_hash, created_at, updated_at) FROM stdin;
1	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	t	0.00000000	0.10000000	esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x	\N	2026-03-01 08:00:00.5+00	2026-03-02 09:00:00.75+00
2	bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4	f	512.25000000	0.50000000	\N	\N	2026-03-03 08:00:00.5+00	2026-03-03 08:00:00.5+00
\.


--
-- Data for Name: payouts; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.payouts (id, miner_address, block_height, amount, confirmed, txid, created_at, paid_at) FROM stdin;
1	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80000	50.00000000	t	coinbase-direct	2026-03-05 11:00:00.9+00	2026-03-06 11:00:00.6+00
2	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80001	50.00012345	t	coinbase-direct	2026-03-05 11:10:00.9+00	2026-03-06 11:10:00.6+00
3	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80002	50.00000000	t	coinbase-direct	2026-03-05 11:20:00.9+00	2026-03-06 11:20:00.6+00
4	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80003	50.00012345	t	coinbase-direct	2026-03-05 11:30:00.9+00	2026-03-06 11:30:00.6+00
5	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80004	50.00000000	t	coinbase-direct	2026-03-05 11:40:00.9+00	2026-03-06 11:40:00.6+00
6	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80005	50.00012345	t	coinbase-direct	2026-03-05 11:50:00.9+00	2026-03-06 11:50:00.6+00
7	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80006	50.00000000	t	coinbase-direct	2026-03-05 12:00:00.9+00	2026-03-06 12:00:00.6+00
8	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80007	50.00012345	f	orphaned	2026-03-05 12:10:00.9+00	\N
9	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80008	50.00000000	t	coinbase-direct	2026-03-05 12:20:00.9+00	2026-03-06 12:20:00.6+00
10	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80009	50.00012345	t	coinbase-direct	2026-03-05 12:30:00.9+00	2026-03-06 12:30:00.6+00
11	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80010	50.00000000	t	coinbase-direct	2026-03-05 12:40:00.9+00	2026-03-06 12:40:00.6+00
12	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80011	50.00012345	t	coinbase-direct	2026-03-05 12:50:00.9+00	2026-03-06 12:50:00.6+00
13	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80012	50.00000000	t	coinbase-direct	2026-03-05 13:00:00.9+00	2026-03-06 13:00:00.6+00
14	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80013	50.00012345	t	coinbase-direct	2026-03-05 13:10:00.9+00	2026-03-06 13:10:00.6+00
15	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80014	50.00000000	t	coinbase-direct	2026-03-05 13:20:00.9+00	2026-03-06 13:20:00.6+00
16	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80015	50.00012345	f	\N	2026-03-05 13:30:00.9+00	\N
17	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80016	50.00000000	f	\N	2026-03-05 13:40:00.9+00	\N
18	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80017	50.00012345	f	\N	2026-03-05 13:50:00.9+00	\N
19	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80018	50.00000000	f	\N	2026-03-05 14:00:00.9+00	\N
20	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	80019	50.00012345	f	\N	2026-03-05 14:10:00.9+00	\N
21	bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4	80003	12.50000000	f		2026-03-05 11:30:00.5+00	\N
22	bitcoincashii:qzet9v4jk2et9v4jk2et9v4jk2et9v4jkg0xty3z5s	80004	0.00000001	\N	\N	2026-03-05 11:40:00.5+00	\N
\.


--
-- Data for Name: payouts_1175; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.payouts_1175 (id, miner_address, block_height, amount, txid, status, batch, paid_at, created_at) FROM stdin;
1	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	3000	0.78125	coinbase-direct	paid	\N	2026-03-09 09:00:00.5+00	2026-03-07 09:00:00.75+00
\.


--
-- Name: blocks_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.blocks_id_seq', 20, true);


--
-- Name: miners_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.miners_id_seq', 2, true);


--
-- Name: payouts_1175_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.payouts_1175_id_seq', 1, true);


--
-- Name: payouts_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.payouts_id_seq', 22, true);


--
-- Name: shares_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.shares_id_seq', 1, false);


--
-- Name: blocks_1175 blocks_1175_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blocks_1175
    ADD CONSTRAINT blocks_1175_pkey PRIMARY KEY (height);


--
-- Name: blocks blocks_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blocks
    ADD CONSTRAINT blocks_hash_key UNIQUE (hash);


--
-- Name: blocks blocks_height_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blocks
    ADD CONSTRAINT blocks_height_key UNIQUE (height);


--
-- Name: blocks blocks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blocks
    ADD CONSTRAINT blocks_pkey PRIMARY KEY (id);


--
-- Name: miners miners_address_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.miners
    ADD CONSTRAINT miners_address_key UNIQUE (address);


--
-- Name: miners miners_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.miners
    ADD CONSTRAINT miners_pkey PRIMARY KEY (id);


--
-- Name: payouts_1175 payouts_1175_miner_address_block_height_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts_1175
    ADD CONSTRAINT payouts_1175_miner_address_block_height_key UNIQUE (miner_address, block_height);


--
-- Name: payouts_1175 payouts_1175_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts_1175
    ADD CONSTRAINT payouts_1175_pkey PRIMARY KEY (id);


--
-- Name: payouts payouts_miner_address_block_height_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_miner_address_block_height_key UNIQUE (miner_address, block_height);


--
-- Name: payouts payouts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_pkey PRIMARY KEY (id);


--
-- Name: idx_blocks_height; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_blocks_height ON public.blocks USING btree (height);


--
-- Name: idx_blocks_miner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_blocks_miner ON public.blocks USING btree (miner_address);


--
-- Name: idx_miners_address; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_miners_address ON public.miners USING btree (address);


--
-- Name: idx_payouts_1175_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_payouts_1175_pending ON public.payouts_1175 USING btree (miner_address) WHERE (status = 'pending'::text);


--
-- Name: idx_payouts_block; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_payouts_block ON public.payouts USING btree (block_height);


--
-- Name: idx_payouts_miner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_payouts_miner ON public.payouts USING btree (miner_address);


--
-- Name: idx_payouts_unpaid; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_payouts_unpaid ON public.payouts USING btree (miner_address) WHERE ((txid IS NULL) OR ((txid)::text = ''::text));


--
-- PostgreSQL database dump complete
--

