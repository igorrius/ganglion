package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
)

// TokenEnv is the environment variable that gives the relay its run's
// token. It travels only in the CLI's MCP config file (mode 0600), never
// in argv (ADR-0036).
const TokenEnv = "GANGLION_MCP_TOKEN"

// Relay connects a CLI's stdio MCP client to the server on the Unix
// socket: it sends token as the first line, then copies lines from in
// to the socket and from the socket to out, each at most MaxLine long.
// It does nothing else and logs nothing.
//
// It returns when the socket side ends: nil when the server closed the
// connection, ErrLineTooLong for an oversize line on either side, or
// the error that stopped it. The end of in only closes the socket's
// write side, so the answers still on their way arrive. A read of in
// that is still blocked when Relay returns ends with in; the relay
// process exits right after.
func Relay(ctx context.Context, socket, token string, in io.Reader, out io.Writer) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("mcp relay: connect: %w", err)
	}
	conn := c.(*net.UnixConn)
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := io.WriteString(conn, token+"\n"); err != nil {
		return fmt.Errorf("mcp relay: send the token: %w", err)
	}
	up := make(chan error, 1)
	go func() {
		err := copyLines(conn, in)
		up <- err // before the close below ends the copy down
		if err != nil {
			_ = conn.Close()
			return
		}
		_ = conn.CloseWrite()
	}()
	down := copyLines(out, conn)
	_ = conn.Close()
	select {
	case err := <-up:
		if err != nil {
			return fmt.Errorf("mcp relay: to the server: %w", err)
		}
	default:
	}
	if down != nil && !errors.Is(down, net.ErrClosed) {
		return fmt.Errorf("mcp relay: from the server: %w", down)
	}
	return ctx.Err()
}

// copyLines copies src to dst a line at a time, each line at most
// MaxLine long. The end of src is nil.
func copyLines(dst io.Writer, src io.Reader) error {
	lines := newLineReader(src, MaxLine)
	for {
		line, err := lines.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := dst.Write(append(line, '\n')); err != nil {
			return err
		}
	}
}
