// Package sqlite3 provides the registration side effect expected by
// gorm.io/driver/sqlite without requiring a C compiler.
//
// The real github.com/mattn/go-sqlite3 package is a cgo-backed driver. KPro uses
// gorm.io/driver/sqlite only as a GORM dialector and runs SQLite tests through
// modernc.org/sqlite, so the only behavior needed from this import path is
// registering a database/sql driver named "sqlite3".
package sqlite3

import (
	"database/sql"

	moderncsqlite "modernc.org/sqlite"
)

func init() {
	sql.Register("sqlite3", &moderncsqlite.Driver{})
}
