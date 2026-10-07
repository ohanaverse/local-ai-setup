package spend

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestMain fails both seams by default, so no test in this package can find
// or run the real psql — and so none can reach a real database, whatever
// connection string the developer's shell or config.yaml holds. A test that
// exercises Query sets the seams itself (stubPsql, or fakePsqlBinary for the
// tests that run a real process: a shell script, never psql).
func TestMain(m *testing.M) {
	lookPath = func(string) (string, error) {
		return "", errors.New("lookPath not stubbed in this test")
	}
	runPsql = func(context.Context, string, []string, []string) ([]byte, []byte, int, error) {
		return nil, nil, 0, errors.New("runPsql not stubbed in this test")
	}
	os.Exit(m.Run())
}
