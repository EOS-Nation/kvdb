package netkv

import (
	"fmt"
	"io/ioutil"
	"path"
	"testing"
	"time"

	"github.com/dfuse-io/kvdb/store"
	_ "github.com/dfuse-io/kvdb/store/badger"
	netkvserver "github.com/dfuse-io/kvdb/store/netkv/server"
	"github.com/dfuse-io/kvdb/store/storetest"
	"github.com/stretchr/testify/require"
)

func TestAll(t *testing.T) {
	storetest.TestAll(t, "NetKV", newTestNetKVFactory(t))
}

func newTestNetKVFactory(t *testing.T) storetest.DriverFactory {
	return func() (store.KVStore, storetest.DriverCleanupFunc) {
		// Start a server
		dir, err := ioutil.TempDir("", "kvdb-netkv-server")
		fmt.Println("MAMA", dir)
		require.NoError(t, err)
		dsn1 := fmt.Sprintf("badger://%s", path.Join(dir, "netkv"))
		server, err := netkvserver.New(":65112", dsn1)
		require.NoError(t, err)
		time.Sleep(100 * time.Millisecond)

		// Setup the `netkv` client, and test it.
		dsn2 := fmt.Sprintf("netkv://localhost:65112?insecure=true")
		kvStore, err := NewStore(dsn2)
		require.NoError(t, err)

		return kvStore, func() {
			server.Close()
			time.Sleep(100 * time.Millisecond)
			// err := os.RemoveAll(dir)
			// require.NoError(t, err)
		}
	}
}