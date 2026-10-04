-- A database Forge Solo 1.0.9 made on Umbrel, for scripts/it-pg-to-sqlite.sh (case 8): its compose's
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
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
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
    paid_at timestamp with time zone,
    status character varying(20) DEFAULT 'pending'::character varying
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
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP
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
-- Name: pool_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pool_config (
    id integer DEFAULT 1 NOT NULL,
    pool_address text DEFAULT ''::text,
    payout_address_1175 text DEFAULT ''::text,
    coinbase_tag text DEFAULT ''::text,
    min_payout double precision DEFAULT 1,
    updated_at timestamp with time zone DEFAULT now()
);


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
1	90000	b19a0a8cf976838e4b06cca1a62704e0a31231d542fa4e0dd0cf67827d655c29	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234567.50000000	confirmed	0	t	2026-06-05 03:00:00.2+00	2026-06-06 03:00:00.8+00
2	90001	97acc4a690767d7a7901f843bdf7b84d89e0b34af4deeda5ef040745c6771dfa	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234568.50000000	confirmed	0	t	2026-06-05 03:10:00.2+00	2026-06-06 03:10:00.8+00
3	90002	785d268ed89cc14ddfcba51db3db534fabecd58f8ae8923c74307307dd0593db	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234569.50000000	confirmed	0	t	2026-06-05 03:20:00.2+00	2026-06-06 03:20:00.8+00
4	90003	b35b5f5067e6fabdcea7a832255ba374bcc88dffd3a804c0adc35474a13713c9	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234570.50000000	orphaned	0	t	2026-06-05 03:30:00.2+00	2026-06-06 03:30:00.8+00
5	90004	800a3ac59636ad41f4265f0136cbf7d539427fd47cea6baae6ff1d418705125d	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234571.50000000	confirmed	0	t	2026-06-05 03:40:00.2+00	2026-06-06 03:40:00.8+00
6	90005	c35c2a62393be327d535eb4193c3b9c6ab448aa97d378467db9d236fb3f40027	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234572.50000000	confirmed	0	t	2026-06-05 03:50:00.2+00	2026-06-06 03:50:00.8+00
7	90006	91fc270897191f8f25469de0ce7b05b8724e5a85f8a8b73934d40710a68104be	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234573.50000000	confirmed	0	t	2026-06-05 04:00:00.2+00	2026-06-06 04:00:00.8+00
8	90007	a60f890888a06cc24e8af34cb9494187aeb228a48e58605ce45eda784945b1f1	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234574.50000000	confirmed	0	t	2026-06-05 04:10:00.2+00	2026-06-06 04:10:00.8+00
9	90008	e36fe230e4232cb45194ef2791cb66a7d79fc3b1cd912f0d30b2b8c6b1812694	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234575.50000000	confirmed	0	t	2026-06-05 04:20:00.2+00	2026-06-06 04:20:00.8+00
10	90009	384257722c963c19ef60c1133e0f172de4f8f531096796b50b2ba12a692ad27d	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234576.50000000	confirmed	0	t	2026-06-05 04:30:00.2+00	2026-06-06 04:30:00.8+00
11	90010	1b07e9e0b0cc4d26520d396d952f0f81db018c38616fcd27d0f891c892913ef0	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234577.50000000	confirmed	0	t	2026-06-05 04:40:00.2+00	2026-06-06 04:40:00.8+00
12	90011	2d0e04fb18c8ccfc03d0f6050d2ec47c7732e3b50f649f56dbfa5bad4e28e784	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234578.50000000	confirmed	0	t	2026-06-05 04:50:00.2+00	2026-06-06 04:50:00.8+00
13	90012	38a4ec9a5f7e94452b87853676f76d53e93afba3a9fb59c9778b451a76a1a962	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234579.50000000	confirmed	0	t	2026-06-05 05:00:00.2+00	2026-06-06 05:00:00.8+00
14	90013	8776a92fc47574b321f1ded3e8be32f3408b70de37128ffb5232d2322dc5896d	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234580.50000000	confirmed	0	t	2026-06-05 05:10:00.2+00	2026-06-06 05:10:00.8+00
15	90014	95bcb58149dccc43f2a63152656d8a386d715687d7ffa093d88757c6e74de5e0	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234581.50000000	confirmed	0	t	2026-06-05 05:20:00.2+00	2026-06-06 05:20:00.8+00
16	90015	f91e55df43f5a0ad52ee53452b86a44e36cdc359fe55f60eac8f300d5db36e95	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234582.50000000	confirmed	0	t	2026-06-05 05:30:00.2+00	2026-06-06 05:30:00.8+00
17	90016	2fd73d0f6061b63ca7e6e44370f50ee621b9217198255571cbba05d3a3964a2b	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234583.50000000	confirmed	0	t	2026-06-05 05:40:00.2+00	2026-06-06 05:40:00.8+00
18	90017	9462b829f38702598cedf118ed4a6c83527b30092cab46b8df843f29d0c1216c	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234584.50000000	confirmed	0	t	2026-06-05 05:50:00.2+00	2026-06-06 05:50:00.8+00
19	90018	e912a65e9d0ee4dfe6ee0bc0ee9922095f747266a515b758644e0a7e3dc11b3c	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234585.50000000	confirmed	0	t	2026-06-05 06:00:00.2+00	2026-06-06 06:00:00.8+00
20	90019	a00bcc3d2334f03c05203d3e06457a2dc17338f599e5969b8932a14f8f11d4b8	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234586.50000000	confirmed	0	t	2026-06-05 06:10:00.2+00	2026-06-06 06:10:00.8+00
21	90020	641959b61ebfe983bd7192e86bf43fd1f6629e023c6dc649abb2d780c86a7a0e	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234587.50000000	pending	0	t	2026-06-05 06:20:00.2+00	\N
22	90021	09a84094390c95cac852ddc5b73576c0337b32b8286d3e628450a61b8e8e12a3	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234588.50000000	pending	0	t	2026-06-05 06:30:00.2+00	\N
23	90022	3725153f1d57820e1947245fe210d54e6dc1a0508536a94e1606bddbf210516e	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234589.50000000	pending	0	t	2026-06-05 06:40:00.2+00	\N
24	90023	005c5bfb9e00a81fbda2df2b83381854c8b1572c6f2b868fce3a767b62d7ad78	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25012345	2234590.50000000	pending	0	t	2026-06-05 06:50:00.2+00	\N
25	90024	69b83b4b8219710f534b18abf8c9907d42495b4fb850386e87a5b72b4a80666c	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	6.25000000	2234591.50000000	pending	0	t	2026-06-05 07:00:00.2+00	\N
\.


--
-- Data for Name: blocks_1175; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.blocks_1175 (height, hash, gross_reward, is_solo, finder, distributed, status, created_at) FROM stdin;
4000	af54cc30b2ec8918a0699278efd8df45	0.78125	t	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	t	confirmed	2026-06-07 01:00:00.5+00
4001	689e931939a448de1b2698cde3814d42	0.78125	f	\N	t	orphaned	2026-06-08 01:00:00.25+00
\.


--
-- Data for Name: miners; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.miners (id, address, solo_mining, manual_diff, min_payout, address_1175, settings_pin_hash, created_at, updated_at) FROM stdin;
1	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	t	0.00000000	0.10000000	esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x	$2a$10$v109v109v109v109v109v.abcdefghijklmnopqrstuvwxyz01234	2026-05-01 00:00:00.5+00	2026-06-02 01:00:00.75+00
\.


--
-- Data for Name: payouts; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.payouts (id, miner_address, block_height, amount, confirmed, txid, created_at, paid_at, status) FROM stdin;
1	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90000	6.25000000	t	coinbase-direct	2026-06-05 03:00:00.7+00	2026-06-05 03:00:00.2+00	paid
2	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90001	6.25012345	t	coinbase-direct	2026-06-05 03:10:00.7+00	2026-06-05 03:10:00.2+00	paid
3	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90002	6.25000000	t	coinbase-direct	2026-06-05 03:20:00.7+00	2026-06-05 03:20:00.2+00	paid
4	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90003	6.25012345	f	orphaned	2026-06-05 03:30:00.7+00	2026-06-05 03:30:00.2+00	orphaned
5	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90004	6.25000000	t	coinbase-direct	2026-06-05 03:40:00.7+00	2026-06-05 03:40:00.2+00	paid
6	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90005	6.25012345	t	coinbase-direct	2026-06-05 03:50:00.7+00	2026-06-05 03:50:00.2+00	paid
7	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90006	6.25000000	t	coinbase-direct	2026-06-05 04:00:00.7+00	2026-06-05 04:00:00.2+00	paid
8	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90007	6.25012345	t	coinbase-direct	2026-06-05 04:10:00.7+00	2026-06-05 04:10:00.2+00	paid
9	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90008	6.25000000	t	coinbase-direct	2026-06-05 04:20:00.7+00	2026-06-05 04:20:00.2+00	paid
10	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90009	6.25012345	t	coinbase-direct	2026-06-05 04:30:00.7+00	2026-06-05 04:30:00.2+00	paid
11	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90010	6.25000000	t	coinbase-direct	2026-06-05 04:40:00.7+00	2026-06-05 04:40:00.2+00	paid
12	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90011	6.25012345	t	coinbase-direct	2026-06-05 04:50:00.7+00	2026-06-05 04:50:00.2+00	paid
13	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90012	6.25000000	t	coinbase-direct	2026-06-05 05:00:00.7+00	2026-06-05 05:00:00.2+00	paid
14	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90013	6.25012345	t	coinbase-direct	2026-06-05 05:10:00.7+00	2026-06-05 05:10:00.2+00	paid
15	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90014	6.25000000	t	coinbase-direct	2026-06-05 05:20:00.7+00	2026-06-05 05:20:00.2+00	paid
16	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90015	6.25012345	t	coinbase-direct	2026-06-05 05:30:00.7+00	2026-06-05 05:30:00.2+00	paid
17	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90016	6.25000000	t	coinbase-direct	2026-06-05 05:40:00.7+00	2026-06-05 05:40:00.2+00	paid
18	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90017	6.25012345	t	coinbase-direct	2026-06-05 05:50:00.7+00	2026-06-05 05:50:00.2+00	paid
19	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90018	6.25000000	t	coinbase-direct	2026-06-05 06:00:00.7+00	2026-06-05 06:00:00.2+00	paid
20	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90019	6.25012345	t	coinbase-direct	2026-06-05 06:10:00.7+00	2026-06-05 06:10:00.2+00	paid
21	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90020	6.25000000	t	coinbase-direct	2026-06-05 06:20:00.7+00	2026-06-05 06:20:00.2+00	paid
22	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90021	6.25012345	t	coinbase-direct	2026-06-05 06:30:00.7+00	2026-06-05 06:30:00.2+00	paid
23	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90022	6.25000000	t	coinbase-direct	2026-06-05 06:40:00.7+00	2026-06-05 06:40:00.2+00	paid
24	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90023	6.25012345	t	coinbase-direct	2026-06-05 06:50:00.7+00	2026-06-05 06:50:00.2+00	paid
25	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	90024	6.25000000	t	coinbase-direct	2026-06-05 07:00:00.7+00	2026-06-05 07:00:00.2+00	paid
26	bitcoincashii:qzet9v4jk2et9v4jk2et9v4jk2et9v4jkg0xty3z5s	90005	1.50000000	f	\N	2026-06-05 03:50:00.5+00	\N	\N
\.


--
-- Data for Name: payouts_1175; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.payouts_1175 (id, miner_address, block_height, amount, txid, status, batch, paid_at, created_at) FROM stdin;
1	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	4000	0.78125	coinbase-direct	paid	b1	2026-06-09 01:00:00.5+00	2026-06-07 01:00:00.75+00
2	bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4	4001	0.5	orphaned	orphaned	\N	\N	2026-06-08 01:00:00.75+00
\.


--
-- Data for Name: pool_config; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.pool_config (id, pool_address, payout_address_1175, coinbase_tag, min_payout, updated_at) FROM stdin;
1	bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2	esf1quhj7te09uhj7te09uhj7te09uhj7te09dnlk6x	/v1.0.9 ö/	0.25	2026-06-01 01:00:00.5+00
2	bitcoincashii:qrpu8s7rc0pu8s7rc0pu8s7rc0pu8s7rcv2v0f4la4			0	2026-06-01 01:00:00+00
\.


--
-- Name: blocks_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.blocks_id_seq', 25, true);


--
-- Name: miners_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.miners_id_seq', 1, true);


--
-- Name: payouts_1175_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.payouts_1175_id_seq', 2, true);


--
-- Name: payouts_id_seq; Type: SEQUENCE SET; Schema: public; Owner: -
--

SELECT pg_catalog.setval('public.payouts_id_seq', 26, true);


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
-- Name: pool_config pool_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pool_config
    ADD CONSTRAINT pool_config_pkey PRIMARY KEY (id);


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

