package databases

import (
	"context"
	"strconv"
)

// redisEngine has no stdout dump. Redis persists by writing an RDB file, so a backup asks the server for one over
// its replication channel and copies the resulting file out, and a restore puts a file back and cycles the
// container — a running Redis serves what it already has in memory and will not load a file over the top of it.
//
// redis-cli --rdb is used rather than BGSAVE plus polling for completion because the transfer is the same one a
// replica performs and the command returns only when the payload is fully written, so there is no state to poll and
// no window in which a copy catches a half-written file.
type redisEngine struct{}

func (redisEngine) Key() string  { return "redis" }
func (redisEngine) Name() string { return "Redis" }
func (redisEngine) Port() int    { return 6379 }

// redisDumpPath is where the RDB is written inside the container before it is copied out. /tmp is used rather than
// /data so a dump never lands in the directory the server persists to and never becomes the file a later restart
// loads by accident.
const redisDumpPath = "/tmp/codeplo-dump.rdb"

func (redisEngine) DumpPlan(_ context.Context, r Resource) (Dump, error) {
	cmd := []string{"redis-cli", "-h", "127.0.0.1", "-p", strconv.Itoa(r.port()), "--rdb", redisDumpPath}
	var env []string
	if pass := r.firstVar("", "REDIS_PASSWORD", "REDISCLI_AUTH"); pass != "" {
		env = []string{"REDISCLI_AUTH=" + pass}
	}
	return Dump{Stdout: false, Cmd: cmd, Env: env, ContainerPath: redisDumpPath, Ext: ".rdb"}, nil
}

// redisDataDir is the volume the images persist to and the only place a restored RDB can be loaded from: the
// server reads dbfilename from dir, which every official and valkey image starts with as /data. A deployment that
// overrides dir is a deployment whose engine is not the one this panel deployed from a template, and the restore
// fails loudly when the server starts and still reports the old keyspace.
const redisDataDir = "/data"

func (redisEngine) RestorePlan(_ context.Context, _ Resource, _ Dump) (Restore, error) {
	return Restore{CopyTo: redisDataDir + "/dump.rdb", Restart: true}, nil
}

func init() { register(redisEngine{}) }

var _ Engine = redisEngine{}
