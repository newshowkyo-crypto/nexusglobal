package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupTestDB(t *testing.T) (*TxManager, func()) {
	if testing.Short() {
		t.Skip("skipping testcontainers test in short mode")
	}

	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "postgres:15",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "test",
			"POSTGRES_DB":       "test",
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("skipping: cannot start container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot get host: %v", err)
	}

	mappedPort, err := container.MappedPort(ctx, "5432")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot get port: %v", err)
	}

	dsn := "postgres://test:test@" + host + ":" + mappedPort.Port() + "/test?sslmode=disable"

	pool, err := NewPool(ctx, dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}

	tm := NewTxManager(pool)

	cleanup := func() {
		pool.Close()
		_ = container.Terminate(ctx)
	}

	return tm, cleanup
}

func TestTxManager_Commit(t *testing.T) {
	tm, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	err := tm.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS test_table (id SERIAL PRIMARY KEY, name VARCHAR(64))")
		return err
	})
	assert.NoError(t, err)

	var exists bool
	err = tm.Pool().QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'test_table')").Scan(&exists)
	assert.NoError(t, err)
	assert.True(t, exists)
}

func TestTxManager_Rollback(t *testing.T) {
	tm, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	err := tm.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS rollback_test (id SERIAL PRIMARY KEY)")
		if err != nil {
			return err
		}
		return assert.AnError
	})
	assert.Error(t, err)

	var exists bool
	err = tm.Pool().QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'rollback_test')").Scan(&exists)
	assert.NoError(t, err)
	assert.False(t, exists)
}

func TestTxManager_ConcurrentIsolation(t *testing.T) {
	tm, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	err := tm.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS concurrent_test (id SERIAL PRIMARY KEY, value INT)")
		return err
	})
	require.NoError(t, err)

	err = tm.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO concurrent_test (value) VALUES (1)")
		return err
	})
	require.NoError(t, err)

	done := make(chan error, 2)

	go func() {
		done <- tm.WithinTx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "UPDATE concurrent_test SET value = 2 WHERE id = 1")
			if err != nil {
				return err
			}
			time.Sleep(100 * time.Millisecond)
			return nil
		})
	}()

	go func() {
		done <- tm.WithinTx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "UPDATE concurrent_test SET value = 3 WHERE id = 1")
			if err != nil {
				return err
			}
			time.Sleep(100 * time.Millisecond)
			return nil
		})
	}()

	err1 := <-done
	err2 := <-done
	assert.NoError(t, err1)
	assert.NoError(t, err2)
}
