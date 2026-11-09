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

	_ "modernc.org/sqlite"
)

const databaseName = "execution.sqlite3"

type Store struct {
	dbMu            sync.RWMutex
	db              *sql.DB
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
		return err
	}
	return s.startAt(ctx, root)
}

func (s *Store) startAt(ctx context.Context, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return errors.New("create execution store directory")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("resolve execution store directory")
	}
	if err := checkFilesystem(root); err != nil {
		return err
	}
	lock, err := openPrivateFile(filepath.Join(root, "execution.lock"))
	if err != nil {
		return errors.New("open execution store lock")
	}
	if err := lockExclusive(lock); err != nil {
		_ = lock.Close()
		return errors.New("execution store lock unavailable")
	}
	s.lock = lock
	db, err := openDatabase(ctx, filepath.Join(root, databaseName))
	if err != nil {
		_ = s.release()
		return err
	}
	report, err := migrate(ctx, db, migrations)
	if err != nil {
		_ = db.Close()
		_ = s.release()
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = db.Close()
		_ = s.release()
		return err
	}
	s.dbMu.Lock()
	s.db = db
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
	s.dbMu.Unlock()
	if db != nil {
		if db.Close() != nil {
			err = errors.New("close execution database")
		}
	}
	if s.lock != nil {
		if s.lock.Close() != nil {
			err = errors.Join(err, errors.New("release execution store lock"))
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
			return nil, errors.New("invalid execution database companion file")
		}
	}
	file, err := openPrivateFile(path)
	if err != nil {
		return nil, errors.New("open execution database file")
	}
	if file.Close() != nil {
		return nil, errors.New("prepare execution database file")
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
		return nil, errors.New("initialize execution database")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := configure(ctx, db); err != nil {
		_ = db.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return db, nil
}

func configure(ctx context.Context, db *sql.DB) error {
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
		return errors.New("enable execution database WAL")
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
			return errors.New("verify execution database connection settings")
		}
	}
	return nil
}
