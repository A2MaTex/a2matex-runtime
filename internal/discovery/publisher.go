package discovery

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Publisher owns the connection file as a supervised lifecycle resource.
type Publisher struct {
	listenAddress    string
	session          *auth.Session
	resolveDirectory func() (string, error)
	done             chan struct{}
	doneOnce         sync.Once
	// These values identify the exact file created by Start. Shutdown checks
	// both before deleting so it cannot remove a replacement written later.
	publishedPath   string
	publishedFile   fs.FileInfo
	publishedDigest [sha256.Size]byte
}

// NewPublisher creates the lifecycle resource that publishes and removes the
// current process's local connection document.
func NewPublisher(cfg config.Config, session *auth.Session) *Publisher {
	return newPublisher(cfg.HTTP.ListenAddress, session, resolveConnectionDirectory)
}

func newPublisher(
	listenAddress string,
	session *auth.Session,
	resolveDirectory func() (string, error),
) *Publisher {
	return &Publisher{
		listenAddress:    listenAddress,
		session:          session,
		resolveDirectory: resolveDirectory,
		done:             make(chan struct{}),
	}
}

// Start publishes the complete connection document after earlier components
// have started.
func (p *Publisher) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Path resolution is delayed until startup so constructor injection remains
	// side-effect free and failures participate in the B3 startup rollback.
	directory, err := p.resolveDirectory()
	if err != nil || !filepath.IsAbs(directory) {
		return xerror.ErrResolveLocalAuthDirectory
	}
	if err := prepareConnectionDirectory(directory); err != nil {
		return err
	}

	document, err := encodeConnectionDocument(p.listenAddress, p.session)
	if err != nil {
		return err
	}

	// App starts the HTTP server before this publisher, so a published endpoint
	// always refers to a listener that has already bound successfully.
	publishedPath, publishedFile, err := publishConnectionDocument(directory, document)
	if err != nil {
		return err
	}

	p.publishedPath = publishedPath
	p.publishedFile = publishedFile
	p.publishedDigest = sha256.Sum256(document)

	return nil
}

// Wait blocks until Shutdown releases the publisher lifecycle resource.
func (p *Publisher) Wait() error {
	<-p.done

	return nil
}

// Shutdown removes only the connection document published by this instance.
func (p *Publisher) Shutdown(ctx context.Context) error {
	// Closing done releases the B3 supervisor's Wait call even when cleanup
	// fails; otherwise the supervisor would wait until its shutdown deadline.
	defer p.doneOnce.Do(func() {
		close(p.done)
	})

	if err := ctx.Err(); err != nil {
		return err
	}
	if p.publishedPath == "" || p.publishedFile == nil {
		return nil
	}

	// Lstat avoids following a symlink placed at the published path. SameFile
	// and the digest ensure this process removes only the document it created.
	currentFile, err := os.Lstat(p.publishedPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || currentFile.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(p.publishedFile, currentFile) {
		return xerror.ErrCleanupLocalAuthConnection
	}
	currentDigest, err := digestConnectionFile(p.publishedPath)
	if err != nil || subtle.ConstantTimeCompare(currentDigest[:], p.publishedDigest[:]) != 1 {
		return xerror.ErrCleanupLocalAuthConnection
	}
	if err := os.Remove(p.publishedPath); err != nil {
		return xerror.ErrCleanupLocalAuthConnection
	}

	return nil
}

func prepareConnectionDirectory(directory string) error {
	// Use Lstat so an existing symlink is rejected instead of silently writing
	// the session token through it into another directory.
	info, err := os.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(directory, constants.ConnectionDirectoryMode); err != nil {
			return xerror.ErrPrepareLocalAuthDirectory
		}
		info, err = os.Lstat(directory)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return xerror.ErrPrepareLocalAuthDirectory
	}

	if runtime.GOOS != "windows" {
		// Restrict discovery data to the current OS user. Windows applies access
		// control through its user profile rather than POSIX permission bits.
		if err := os.Chmod(directory, constants.ConnectionDirectoryMode); err != nil {
			return xerror.ErrPrepareLocalAuthDirectory
		}
	}

	return nil
}

func publishConnectionDocument(directory string, document []byte) (string, fs.FileInfo, error) {
	finalPath := filepath.Join(directory, constants.RuntimeConnectionFilename)
	if info, err := os.Lstat(finalPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			return "", nil, xerror.ErrPublishLocalAuthConnection
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", nil, xerror.ErrPublishLocalAuthConnection
	}

	// Build the complete document beside the destination, then rename it. Local
	// clients can observe a missing file or a complete JSON document, never a
	// partially written token.
	temporaryFile, err := os.CreateTemp(directory, ".runtime-connection-*.tmp")
	if err != nil {
		return "", nil, xerror.ErrPublishLocalAuthConnection
	}
	temporaryPath := temporaryFile.Name()
	temporaryOwned := true
	defer func() {
		if temporaryOwned {
			_ = os.Remove(temporaryPath)
		}
	}()

	if runtime.GOOS != "windows" {
		if err := temporaryFile.Chmod(constants.ConnectionFileMode); err != nil {
			_ = temporaryFile.Close()
			return "", nil, xerror.ErrPublishLocalAuthConnection
		}
	}

	written, writeErr := temporaryFile.Write(document)
	closeErr := temporaryFile.Close()
	if writeErr != nil || written != len(document) || closeErr != nil {
		return "", nil, xerror.ErrPublishLocalAuthConnection
	}

	if err := os.Remove(finalPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", nil, xerror.ErrPublishLocalAuthConnection
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return "", nil, xerror.ErrPublishLocalAuthConnection
	}
	temporaryOwned = false

	finalInfo, err := os.Lstat(finalPath)
	if err != nil {
		_ = os.Remove(finalPath)
		return "", nil, xerror.ErrPublishLocalAuthConnection
	}
	if finalInfo.Mode()&os.ModeSymlink != 0 || !finalInfo.Mode().IsRegular() {
		_ = os.Remove(finalPath)
		return "", nil, xerror.ErrPublishLocalAuthConnection
	}

	return finalPath, finalInfo, nil
}

func digestConnectionFile(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, xerror.ErrCleanupLocalAuthConnection
	}

	// The document is tiny. A fixed limit prevents an unexpectedly replaced
	// file from causing unbounded allocation during shutdown.
	contents, readErr := io.ReadAll(io.LimitReader(file, constants.MaxConnectionFileBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(contents) > constants.MaxConnectionFileBytes {
		return [sha256.Size]byte{}, xerror.ErrCleanupLocalAuthConnection
	}

	return sha256.Sum256(contents), nil
}
