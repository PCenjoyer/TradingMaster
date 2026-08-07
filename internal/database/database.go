package database

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationLockID int64 = 8401742630183851

func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("TM_DATABASE_URL имеет некорректный формат")
	}
	config.MaxConns = 10
	config.MinConns = 1
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["application_name"] = "tradingmaster"

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("создать пул PostgreSQL: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("подключиться к PostgreSQL: %w", err)
	}
	return pool, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("получить соединение для миграций: %w", err)
	}
	defer connection.Release()

	if _, err := connection.Exec(ctx, "select pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("заблокировать миграции: %w", err)
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = connection.Exec(unlockContext, "select pg_advisory_unlock($1)", migrationLockID)
	}()

	if _, err := connection.Exec(ctx, `
		create table if not exists tradingmaster_schema_migrations (
			name text primary key,
			checksum text not null,
			applied_at timestamptz not null default now()
		)`); err != nil {
		return fmt.Errorf("подготовить журнал миграций: %w", err)
	}

	entries, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("прочитать список миграций: %w", err)
	}
	sort.Strings(entries)
	for _, name := range entries {
		body, readErr := migrations.ReadFile(name)
		if readErr != nil {
			return fmt.Errorf("прочитать миграцию %s: %w", name, readErr)
		}
		checksumBytes := sha256.Sum256(body)
		checksum := hex.EncodeToString(checksumBytes[:])

		var storedChecksum string
		err = connection.QueryRow(ctx,
			"select checksum from tradingmaster_schema_migrations where name = $1", name,
		).Scan(&storedChecksum)
		switch {
		case err == nil && storedChecksum != checksum:
			return fmt.Errorf("миграция %s была изменена после применения", name)
		case err == nil:
			continue
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("проверить миграцию %s: %w", name, err)
		}

		transaction, beginErr := connection.Begin(ctx)
		if beginErr != nil {
			return fmt.Errorf("начать миграцию %s: %w", name, beginErr)
		}
		if _, err = transaction.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err == nil {
			_, err = transaction.Exec(ctx,
				"insert into tradingmaster_schema_migrations(name, checksum) values ($1, $2)",
				name, checksum,
			)
		}
		if err == nil {
			err = transaction.Commit(ctx)
		} else {
			_ = transaction.Rollback(ctx)
		}
		if err != nil {
			return fmt.Errorf("применить миграцию %s: %w", name, err)
		}
	}
	return nil
}
