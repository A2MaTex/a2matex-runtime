package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const databaseName = "execution.sqlite3"

type Store struct {
	dbMu            sync.RWMutex
	db              *sql.DB
	stateDir        string
	lock            *os.File
	done            chan struct{}
	migrationReport MigrationReport
}

func New() *Store {
	return &Store{done: make(chan struct{})}
}

func (s *Store) Start(ctx context.Context) error {
	root, err := stateDirectory()
	if err != nil {
		return xerror.WithOperation("resolve_state_directory", err)
	}
	return s.startAt(ctx, root)
}

func (s *Store) startAt(ctx context.Context, root string) error {
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("open_execution_store", err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return storeFailure("create_store_directory", "create execution store directory", err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return storeFailure("resolve_store_directory", "resolve execution store directory", err)
	}
	if err := checkFilesystem(root); err != nil {
		return xerror.WithOperation("check_store_filesystem", err)
	}
	lock, err := openPrivateFile(filepath.Join(root, "execution.lock"))
	if err != nil {
		return storeFailure("open_store_lock", "open execution store lock", err)
	}
	if err := lockExclusive(lock); err != nil {
		_ = lock.Close()
		if isLockContention(err) {
			return xerror.WithOperation("acquire_store_lock", xerror.ErrExecutionStoreInUse)
		}
		return storeFailure("acquire_store_lock", "acquire execution store lock", err)
	}
	s.lock = lock
	db, err := openDatabase(ctx, filepath.Join(root, databaseName))
	if err != nil {
		_ = s.release()
		return xerror.WithOperation("open_execution_store", err)
	}
	report, err := migrate(ctx, db, migrations)
	if err != nil {
		_ = db.Close()
		_ = s.release()
		return xerror.WithOperation("migrate_execution_store", err)
	}
	if err := ctx.Err(); err != nil {
		_ = db.Close()
		_ = s.release()
		return xerror.WithOperation("open_execution_store", err)
	}
	s.dbMu.Lock()
	s.db = db
	s.stateDir = root
	s.migrationReport = report
	s.dbMu.Unlock()
	return nil
}

func (s *Store) Wait() error {
	<-s.done
	return nil
}

func (s *Store) Shutdown(_ context.Context) error {
	err := s.release()
	close(s.done)
	return err
}

func (s *Store) release() error {
	var err error
	s.dbMu.Lock()
	db := s.db
	s.db = nil
	s.stateDir = ""
	s.dbMu.Unlock()
	if db != nil {
		if closeErr := db.Close(); closeErr != nil {
			err = storeFailure("close_execution_database", "close execution database", closeErr)
		}
	}
	if s.lock != nil {
		if closeErr := s.lock.Close(); closeErr != nil {
			err = errors.Join(err, storeFailure("release_store_lock", "release execution store lock", closeErr))
		}
		s.lock = nil
	}
	return err
}

// DB returns the current database handle to SQLite adapters, or nil before startup.
func (s *Store) DB() *sql.DB {
	s.dbMu.RLock()
	defer s.dbMu.RUnlock()
	return s.db
}

// StateDirectory returns the canonical state directory while the store holds its lock.
func (s *Store) StateDirectory() string {
	s.dbMu.RLock()
	defer s.dbMu.RUnlock()
	return s.stateDir
}

func openPrivateFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("execution store path is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
}

func openDatabase(ctx context.Context, path string) (*sql.DB, error) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, storeFailure("check_database_companion_files", "invalid execution database companion file", err)
		}
	}
	file, err := openPrivateFile(path)
	if err != nil {
		return nil, storeFailure("open_execution_database", "open execution database file", err)
	}
	if err := file.Close(); err != nil {
		return nil, storeFailure("prepare_database_file", "prepare execution database file", err)
	}

	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	settings := url.Values{}
	for _, pragma := range []string{"busy_timeout(2000)", "foreign_keys(ON)", "synchronous(FULL)"} {
		settings.Add("_pragma", pragma)
	}
	uri.RawQuery = settings.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, storeFailure("initialize_execution_database", "initialize execution database", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := configure(ctx, db); err != nil {
		_ = db.Close()
		if ctx.Err() != nil {
			return nil, xerror.WithOperation("configure_execution_database", ctx.Err())
		}
		return nil, err
	}
	return db, nil
}

func configure(ctx context.Context, db *sql.DB) error {
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
		return storeFailure("enable_database_wal", "enable execution database WAL", err)
	}
	for _, setting := range []struct {
		query string
		value int
	}{
		{"PRAGMA foreign_keys", 1},
		{"PRAGMA synchronous", 2},
		{"PRAGMA busy_timeout", 2000},
	} {
		var actual int
		if err := db.QueryRowContext(ctx, setting.query).Scan(&actual); err != nil || actual != setting.value {
			return storeFailure("verify_database_settings", "verify execution database connection settings", err)
		}
	}
	return nil
}
