package databases

import (
	"context"
	"fmt"
	"strconv"
)

// postgresEngine dumps a whole cluster with pg_dumpall, roles included, and restores it with psql.
//
// Two choices are worth their reasons. The dump is uncompressed inside the container and gzipped by the panel on
// its way to disk, because the only way to pipe pg_dumpall through gzip in one argv array is to run it through a
// shell, and the database name and user are operator-supplied variables — interpolating them into a shell string
// would be an injection point on every scheduled backup. And the connection is made over loopback TCP with the
// password in PGPASSWORD rather than over the unix socket, because socket connections authenticate with peer
// credentials, which match only when the exec happens to run as the same OS user that owns the socket; the images
// this panel deploys all listen on loopback and accept a password there.
type postgresEngine struct{}

func (postgresEngine) Key() string  { return "postgres" }
func (postgresEngine) Name() string { return "PostgreSQL" }
func (postgresEngine) Port() int    { return 5432 }

func (postgresEngine) DumpPlan(_ context.Context, r Resource) (Dump, error) {
	conn, err := newPGConn(r)
	if err != nil {
		return Dump{}, err
	}
	return Dump{
		Stdout: true,
		Cmd:    append([]string{"pg_dumpall", "--clean", "--if-exists", "-w"}, conn.args()...),
		Env:    conn.env(),
		Ext:    ".sql.gz",
	}, nil
}

func (postgresEngine) RestorePlan(_ context.Context, r Resource, d Dump) (Restore, error) {
	conn, err := newPGConn(r)
	if err != nil {
		return Restore{}, err
	}
	// ON_ERROR_STOP makes a half-applied restore fail instead of reporting success, which is the difference between
	// a restore the operator can trust and a database in an unknown state.
	return Restore{
		Stdin: append([]string{"psql", "-w", "-q", "-v", "ON_ERROR_STOP=1"}, conn.args()...),
		Env:   conn.env(),
	}, nil
}

// pgConn is how a dump tool reaches the server: the credentials the resource was deployed with, on loopback.
type pgConn struct {
	host string
	port int
	user string
	pass string
}

func (c pgConn) args() []string {
	return []string{"-h", c.host, "-p", strconv.Itoa(c.port), "-U", c.user}
}

// env returns the connection as environment entries. The password is the one thing here that never belongs in argv:
// argv is what the panel logs and what container inspection shows, while the environment is only visible to the
// dump process and the container's own user.
func (c pgConn) env() []string {
	return []string{"PGHOST=" + c.host, "PGPORT=" + strconv.Itoa(c.port), "PGUSER=" + c.user, "PGPASSWORD=" + c.pass}
}

// newPGConn reads the credentials off the resource's runtime environment.
//
// The names are the ones the official image documents, with the hand-written-compose alternatives next to them. A
// resource that was deployed without a password variable at all is a resource whose dump cannot authenticate, and
// that is said here rather than discovered as "password authentication failed" three minutes into a job.
func newPGConn(r Resource) (pgConn, error) {
	c := pgConn{
		host: "127.0.0.1",
		port: r.port(),
		user: r.firstVar("postgres", "POSTGRES_USER", "PGUSER", "DB_USER"),
		pass: r.firstVar("", "POSTGRES_PASSWORD", "PGPASSWORD", "DB_PASSWORD"),
	}
	if c.pass == "" {
		return c, Conflict{Msg: fmt.Sprintf("%s has no POSTGRES_PASSWORD runtime variable, so a dump cannot authenticate", r.Name)}
	}
	return c, nil
}

func init() { register(postgresEngine{}) }

var _ Engine = postgresEngine{}
