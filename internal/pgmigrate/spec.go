// Package pgmigrate moves the data of an earlier Forge Solo's PostgreSQL database into forgesolo.db,
// the SQLite file every platform runs on from 1.0.13, once. It reads PostgreSQL in one read-only
// snapshot, builds <db>.migrating with the app's own schema (stats.InitDB), proves the copy complete,
// and only then puts it in the database's place. After a return to 1.0.12 and back, it merges what
// each version recorded.
//
// This file is what is carried and how: every table and column, the kind each column holds, the
// value a column an older database lacks gets, and what is left behind and why. drift_test.go keeps
// it in step with the schema the app creates and with 1.0.12's, frozen in testdata.
package pgmigrate

// Kind is what a column holds. It decides the conversion, the stored type the copy checks for and
// whether the column is summed in satoshis.
type Kind int

const (
	KindInt   Kind = iota // INTEGER: ids and heights
	KindText              // TEXT, byte for byte; NULL and '' stay distinct
	KindBool              // INTEGER 1 or 0; NULL stays NULL
	KindMoney             // REAL: an amount of coin, checked in satoshis
	KindFloat             // REAL
	KindTime              // TEXT, UTC "YYYY-MM-DD HH:MM:SS", rounded to the second
)

func (k Kind) String() string {
	switch k {
	case KindInt:
		return "int"
	case KindText:
		return "text"
	case KindBool:
		return "bool01"
	case KindMoney:
		return "money"
	case KindFloat:
		return "float"
	case KindTime:
		return "time"
	}
	return "unknown"
}

// Column is one column carried from PostgreSQL to SQLite.
type Column struct {
	PG      string // its name in PostgreSQL
	SQLite  string // its name in forgesolo.db
	Kind    Kind
	Default any // what it gets when the PostgreSQL table lacks it: 1.0.12's own ADD COLUMN default
}

// Table is one table carried from PostgreSQL, in the order the copy writes them.
type Table struct {
	Name      string
	Key       []string          // orders the rows for the read-back: the row id, or the height
	Natural   []string          // what names a row on both sides: compared as key sets
	Singleton bool              // only the row with id = 1 is copied; SQLite allows no other
	Columns   []Column          // in the order they are written
	Dropped   map[string]string // PostgreSQL columns left behind, and why
}

func col(name string, k Kind) Column { return Column{PG: name, SQLite: name, Kind: k} }

func colDefault(name string, k Kind, def any) Column {
	return Column{PG: name, SQLite: name, Kind: k, Default: def}
}

const (
	droppedMinPayout = "the pool-style minimum payout: a solo block pays its finder in its own coinbase, so nothing reads it (1.0.9 and older)"
	droppedCached    = "a cached sum nothing ever read: the dashboard sums payouts as it reads them"
	droppedNeverUsed = "nothing has ever written or read it"
)

// Tables is every table the move copies, in the order it writes them: settings first, then the
// ledgers.
var Tables = []Table{
	{
		Name: "pool_config", Key: []string{"id"}, Natural: []string{"id"}, Singleton: true,
		Columns: []Column{
			col("id", KindInt),
			col("pool_address", KindText),
			col("payout_address_1175", KindText),
			col("coinbase_tag", KindText),
			colDefault("payout_mode", KindText, "solo"),
			col("updated_at", KindTime),
		},
		Dropped: map[string]string{"min_payout": droppedMinPayout},
	},
	{
		// Absent in 1.0.11 and older: a gateway key is made at the first use of TIDES.
		Name: "datum_identity", Key: []string{"id"}, Natural: []string{"id"}, Singleton: true,
		Columns: []Column{
			col("id", KindInt),
			col("key_seed", KindText),
			col("created_at", KindTime),
		},
	},
	{
		Name: "miners", Key: []string{"id"}, Natural: []string{"address"},
		Columns: []Column{
			col("id", KindInt),
			col("address", KindText),
			col("solo_mining", KindBool),
			col("manual_diff", KindFloat),
			col("address_1175", KindText),
			col("settings_pin_hash", KindText),
			col("created_at", KindTime),
			col("updated_at", KindTime),
		},
		Dropped: map[string]string{
			"min_payout": droppedMinPayout,
			"balance":    droppedCached,
			"total_paid": droppedCached,
		},
	},
	{
		Name: "blocks", Key: []string{"id"}, Natural: []string{"height", "hash"},
		Columns: []Column{
			col("id", KindInt),
			col("height", KindInt),
			col("hash", KindText),
			col("miner_address", KindText),
			col("reward", KindMoney),
			col("status", KindText),
			colDefault("is_solo", KindBool, int64(0)),
			col("created_at", KindTime),
			col("confirmed_at", KindTime),
		},
		Dropped: map[string]string{
			"difficulty":    droppedNeverUsed,
			"confirmations": droppedNeverUsed,
		},
	},
	{
		Name: "payouts", Key: []string{"id"}, Natural: []string{"miner_address", "block_height"},
		Columns: []Column{
			col("id", KindInt),
			col("miner_address", KindText),
			col("block_height", KindInt),
			col("amount", KindMoney),
			col("confirmed", KindBool),
			colDefault("status", KindText, "pending"),
			col("txid", KindText),
			col("created_at", KindTime),
			col("paid_at", KindTime),
		},
	},
	{
		Name: "blocks_1175", Key: []string{"height"}, Natural: []string{"height"},
		Columns: []Column{
			col("height", KindInt),
			col("hash", KindText),
			col("gross_reward", KindMoney),
			colDefault("is_solo", KindBool, int64(0)),
			col("finder", KindText),
			colDefault("distributed", KindBool, int64(0)),
			col("status", KindText),
			col("created_at", KindTime),
		},
	},
	{
		Name: "payouts_1175", Key: []string{"id"}, Natural: []string{"miner_address", "block_height"},
		Columns: []Column{
			col("id", KindInt),
			col("miner_address", KindText),
			col("block_height", KindInt),
			col("amount", KindMoney),
			col("txid", KindText),
			col("status", KindText),
			col("batch", KindText),
			col("paid_at", KindTime),
			col("created_at", KindTime),
		},
	},
}

// NotCopied is every PostgreSQL table the move leaves behind, and why. It never reads them: the
// shares of an install that stored them for weeks are hundreds of TimescaleDB chunks.
var NotCopied = map[string]string{
	"shares":     "the solo shares 1.0.12 stored and nothing read; 1.0.13 stores none",
	"pool_stats": droppedNeverUsed,
}

// metaTable is the migrator's own record of a move, inside the database it made.
const metaTable = "migration_meta"

// TableRules is every table forgesolo.db can hold and what the move does with it. A table the app
// starts to create fails drift_test.go until it has a rule here, so no release can add a table that
// a merge then drops without anyone deciding so.
var TableRules = map[string]string{
	"pool_config":     "copied from PostgreSQL",
	"datum_identity":  "copied from PostgreSQL",
	"miners":          "copied from PostgreSQL",
	"blocks":          "copied from PostgreSQL",
	"payouts":         "copied from PostgreSQL",
	"blocks_1175":     "copied from PostgreSQL",
	"payouts_1175":    "copied from PostgreSQL",
	"shares":          "carried from the existing forgesolo.db during a merge; PostgreSQL's are left behind (NotCopied)",
	"best_shares":     "carried from the existing forgesolo.db during a merge; PostgreSQL has none (1.0.12 kept no best share)",
	"migration_meta":  "the migrator's own record of a move, written fresh by every prepare",
	"sqlite_sequence": "SQLite's own: it follows the ids the copy keeps",
}

// NotFromPostgres lists the columns the app creates in a copied table that the move does not fill
// from PostgreSQL, with the reason. None today: every column is carried.
var NotFromPostgres = map[string]string{}

// table returns the spec of the named copied table.
func table(name string) (Table, bool) {
	for _, t := range Tables {
		if t.Name == name {
			return t, true
		}
	}
	return Table{}, false
}
