package kvlite

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxRESPBulkBytes = 2 * 1024 * 1024
	maxRESPArrayLen  = 4096
)

var (
	ErrUnavailable = errors.New("KVLite vector backend unavailable")
	ErrProtocol    = errors.New("KVLite vector backend protocol error")
)

type respError struct{ message string }

func (e *respError) Error() string { return e.message }

type respValue struct {
	text  string
	array []respValue
	isNil bool
}

type respConn struct {
	conn net.Conn
	read *bufio.Reader
}

func dialRESP(ctx context.Context, address string, timeout time.Duration) (*respConn, error) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("%w: dial %s: %v", ErrUnavailable, address, err)
	}
	return &respConn{conn: conn, read: bufio.NewReader(conn)}, nil
}

func (c *respConn) close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *respConn) roundTrip(ctx context.Context, writeTimeout, readTimeout time.Duration, args ...string) (respValue, error) {
	if c == nil || c.conn == nil {
		return respValue{}, fmt.Errorf("%w: nil connection", ErrUnavailable)
	}
	if err := c.conn.SetWriteDeadline(operationDeadline(ctx, writeTimeout)); err != nil {
		return respValue{}, fmt.Errorf("%w: set write deadline: %v", ErrUnavailable, err)
	}
	if err := writeRESPArray(c.conn, args); err != nil {
		return respValue{}, fmt.Errorf("%w: write command: %v", ErrUnavailable, err)
	}
	if err := c.conn.SetReadDeadline(operationDeadline(ctx, readTimeout)); err != nil {
		return respValue{}, fmt.Errorf("%w: set read deadline: %v", ErrUnavailable, err)
	}
	value, err := readRESPValue(c.read)
	if err != nil {
		var serverErr *respError
		if errors.As(err, &serverErr) {
			if strings.Contains(strings.ToLower(serverErr.message), "vector not initialized") {
				return respValue{}, fmt.Errorf("%w: %s", ErrNotReady, serverErr.message)
			}
			return respValue{}, fmt.Errorf("%w: %s", ErrProtocol, serverErr.message)
		}
		return respValue{}, fmt.Errorf("%w: read response: %v", ErrUnavailable, err)
	}
	return value, nil
}

func operationDeadline(ctx context.Context, configured time.Duration) time.Time {
	deadline := time.Now().Add(configured)
	if ctx != nil {
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			return contextDeadline
		}
	}
	return deadline
}

func writeRESPArray(writer io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	var output bytes.Buffer
	_, _ = fmt.Fprintf(&output, "*%d\r\n", len(args))
	for _, arg := range args {
		_, _ = fmt.Fprintf(&output, "$%d\r\n", len(arg))
		output.WriteString(arg)
		output.WriteString("\r\n")
	}
	_, err := writer.Write(output.Bytes())
	return err
}

func readRESPValue(reader *bufio.Reader) (respValue, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return respValue{}, err
	}
	switch prefix {
	case '+':
		text, err := readRESPLine(reader)
		return respValue{text: text}, err
	case '-':
		text, err := readRESPLine(reader)
		if err != nil {
			return respValue{}, err
		}
		return respValue{}, &respError{message: text}
	case ':':
		text, err := readRESPLine(reader)
		if err != nil {
			return respValue{}, err
		}
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return respValue{}, fmt.Errorf("invalid integer response")
		}
		return respValue{text: text}, nil
	case '$':
		return readRESPBulk(reader)
	case '*':
		return readRESPArray(reader)
	default:
		return respValue{}, fmt.Errorf("unknown response type %q", prefix)
	}
}

func readRESPBulk(reader *bufio.Reader) (respValue, error) {
	lengthText, err := readRESPLine(reader)
	if err != nil {
		return respValue{}, err
	}
	length, err := strconv.Atoi(lengthText)
	if err != nil || length < -1 || length > maxRESPBulkBytes {
		return respValue{}, fmt.Errorf("invalid bulk response length")
	}
	if length == -1 {
		return respValue{isNil: true}, nil
	}
	payload := make([]byte, length+2)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return respValue{}, err
	}
	if payload[length] != '\r' || payload[length+1] != '\n' {
		return respValue{}, fmt.Errorf("malformed bulk response terminator")
	}
	return respValue{text: string(payload[:length])}, nil
}

func readRESPArray(reader *bufio.Reader) (respValue, error) {
	lengthText, err := readRESPLine(reader)
	if err != nil {
		return respValue{}, err
	}
	length, err := strconv.Atoi(lengthText)
	if err != nil || length < -1 || length > maxRESPArrayLen {
		return respValue{}, fmt.Errorf("invalid array response length")
	}
	if length == -1 {
		return respValue{isNil: true}, nil
	}
	values := make([]respValue, 0, length)
	for index := 0; index < length; index++ {
		value, err := readRESPValue(reader)
		if err != nil {
			return respValue{}, err
		}
		values = append(values, value)
	}
	return respValue{array: values}, nil
}

func readRESPLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) > maxRESPBulkBytes || !strings.HasSuffix(line, "\r\n") {
		return "", fmt.Errorf("malformed RESP line")
	}
	return strings.TrimSuffix(line, "\r\n"), nil
}

// respPool is deliberately independent from chat-context's normal-key pool.
// A checked-out connection serves exactly one V command at a time.
type respPool struct {
	address        string
	connectTimeout time.Duration
	maxSize        int
	maxIdle        int

	mu     sync.Mutex
	idle   []*respConn
	total  int
	closed bool
	wake   chan struct{}
}

func newRESPPool(address string, connectTimeout time.Duration, maxSize, maxIdle int) *respPool {
	return &respPool{
		address:        address,
		connectTimeout: connectTimeout,
		maxSize:        maxSize,
		maxIdle:        maxIdle,
		wake:           make(chan struct{}, 1),
	}
}

func (p *respPool) acquire(ctx context.Context) (*respConn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, fmt.Errorf("%w: pool is closed", ErrUnavailable)
		}
		if count := len(p.idle); count > 0 {
			conn := p.idle[count-1]
			p.idle = p.idle[:count-1]
			p.mu.Unlock()
			return conn, nil
		}
		if p.total < p.maxSize {
			p.total++
			p.mu.Unlock()
			conn, err := dialRESP(ctx, p.address, p.connectTimeout)
			if err == nil {
				return conn, nil
			}
			p.discard(nil)
			return nil, err
		}
		wake := p.wake
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: acquire connection: %v", ErrUnavailable, ctx.Err())
		case <-wake:
		}
	}
}

func (p *respPool) release(conn *respConn, reusable bool) {
	if conn == nil {
		return
	}
	p.mu.Lock()
	if p.closed || !reusable || len(p.idle) >= p.maxIdle {
		p.mu.Unlock()
		p.discard(conn)
		return
	}
	p.idle = append(p.idle, conn)
	p.signal()
	p.mu.Unlock()
}

func (p *respPool) discard(conn *respConn) {
	if conn != nil {
		conn.close()
	}
	p.mu.Lock()
	if p.total > 0 {
		p.total--
	}
	p.signal()
	p.mu.Unlock()
}

func (p *respPool) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *respPool) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.total -= len(idle)
	p.signal()
	p.mu.Unlock()
	for _, conn := range idle {
		conn.close()
	}
}
