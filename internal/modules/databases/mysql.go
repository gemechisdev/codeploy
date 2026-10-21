package databases

import (
	"context"
	"fmt"
	"strconv"
)

// mysqlEngine dumps every database with mysqldump and replays the SQL with the mysql client.
//
// The tool list is deliberately the intersection of Oracle MySQL and MariaDB, because both answer to the "mysql"
// engine key: --single-transaction gives a consistent snapshot from InnoDB without locking, --quick streams one
// row at a time so a large table does not need the whole dump in the client's memory, and --routines/--triggers
// keep the dump restorable as a whole database rather than as tables plus a mystery about why the stored
// procedures are gone. Flags only one vendor has — --column-statistics, --no-tablespaces — are avoided in favour
// of the documented default that the dump runs as the account the image was initialized with, which can read
// everything it needs.
type mysqlEngine struct{}

func (mysqlEngine) Key() string  { return "mysql" }
func (mysqlEngine) Name() string { return "MySQL" }
func (mysqlEngine) Port() int    { return 3306 }

func (mysqlEngine) DumpPlan(_ context.Context, r Resource) (Dump, error) {
	conn, err := newMyConn(r)
	if err != nil {
		return Dump{}, err
	}
	cmd := append([]string{"mysqldump", "--all-databases", "--single-transaction", "--quick", "--routines", "--triggers"}, conn.argv...)
	return Dump{Stdout: true, Cmd: cmd, Env: conn.env(), Ext: ".sql.gz"}, nil
}

func (mysqlEngine) RestorePlan(_ context.Context, r Resource, _ Dump) (Restore, error) {
	conn, err := newMyConn(r)
	if err != nil {
		return Restore{}, err
	}
	// The mysql client reads a script from stdin when it has no -e/-f and stdin is not a terminal, which is exactly
	// the shape a streamed restore is.
	return Restore{Stdin: conn.argv, Env: conn.env()}, nil
}

// myConn is the loopback connection and its credential. MYSQL_PWD is the client library's documented environment
// fallback, which is what keeps the password out of argv.
type myConn struct {
	argv []string
	pass string
}

func (c myConn) env() []string { return []string{"MYSQL_PWD=" + c.pass} }

func newMyConn(r Resource) (myConn, error) {
	pass := r.firstVar("", "MYSQL_ROOT_PASSWORD", "MARIADB_ROOT_PASSWORD", "MYSQL_PASSWORD", "DB_PASSWORD")
	if pass == "" {
		return myConn{}, Conflict{Msg: fmt.Sprintf("%s has no MYSQL_ROOT_PASSWORD runtime variable, so a dump cannot authenticate", r.Name)}
	}
	user := r.firstVar("root", "MYSQL_ROOT_USER", "MYSQL_USER", "DB_USER")
	return myConn{
		argv: []string{"--protocol=TCP", "-h", "127.0.0.1", "-P", strconv.Itoa(r.port()), "-u", user},
		pass: pass,
	}, nil
}

func init() { register(mysqlEngine{}) }

var _ Engine = mysqlEngine{}
