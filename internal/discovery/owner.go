package discovery

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const shutdownCommand = "shutdown\n"

// Owner holds the child ends of the two private pipes inherited at launch.
type Owner struct {
	commands *os.File
	ready    *os.File
}

func OpenOwner(commandHandle, readyHandle uintptr) (*Owner, error) {
	if commandHandle == readyHandle || commandHandle < 3 || readyHandle < 3 {
		return nil, errors.New("managed launch requires two distinct inherited pipes")
	}

	commands := os.NewFile(commandHandle, "runtime-owner-commands")
	ready := os.NewFile(readyHandle, "runtime-owner-ready")
	if !isPipe(commands) || !isPipe(ready) {
		if commands != nil {
			_ = commands.Close()
		}
		if ready != nil {
			_ = ready.Close()
		}
		return nil, errors.New("managed launch requires valid inherited pipes")
	}
	preventOwnerPipeInheritance(commands, ready)
	return &Owner{commands: commands, ready: ready}, nil
}

func isPipe(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeNamedPipe != 0
}

// Watch cancels the supervisor on shutdown, owner loss, or a malformed
// control message. The command pipe must be monitored during startup too.
func (o *Owner) Watch(cancel context.CancelCauseFunc) {
	reader := bufio.NewReaderSize(o.commands, 32)
	// A single frame suffices. EOF and invalid frames both fail closed.
	frame, err := reader.ReadSlice('\n')
	reason := "owner_protocol_error"
	cause := xerror.ErrOwnerProtocol
	switch {
	case err == nil && string(frame) == shutdownCommand:
		reason = "owner_shutdown_requested"
		cause = xerror.ErrOwnerShutdownRequested
	case errors.Is(err, io.EOF) && len(frame) == 0:
		reason = "owner_disconnected"
		cause = xerror.ErrOwnerDisconnected
	}
	level := slog.LevelInfo
	if errors.Is(cause, xerror.ErrOwnerProtocol) {
		level = slog.LevelError
	}
	slog.Log(context.Background(), level, "managed Runtime control channel stopped", "component", "runtime", "event", reason)
	cancel(cause)
}

func (o *Owner) Close() {
	_ = o.commands.Close()
	_ = o.ready.Close()
}

func (o *Owner) publish(document []byte) error {
	// This one small frame fits into the empty inherited pipe buffer.
	_, err := o.ready.Write(append(document, '\n'))
	if err != nil {
		return errors.New("publish managed Runtime readiness")
	}
	return nil
}
