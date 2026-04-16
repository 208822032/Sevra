package qa

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-chat-service/pkg/config"
)

var (
	ErrKVLiteTimeout   = errors.New("kvlite qa backend timeout")
	ErrKVLiteConnError = errors.New("kvlite qa backend connection error")
)

type kvliteClient struct {
	addr           string
	connectTimeout time.Duration
	readTimeout    time.Duration
	writeTimeout   time.Duration
	maxRetries     int
	retryBackoff   time.Duration
	pool           *kvliteConnPool
}

func newKVLiteBackend(cnf *config.Config, namespace string) kvliteBackend {
	if cnf == nil {
		return nil
	}
	ctxCfg := cnf.Context.KVLite
	defaultTimeout := time.Duration(ctxCfg.TimeoutMS) * time.Millisecond
	if defaultTimeout <= 0 {
		defaultTimeout = 2 * time.Second
	}
	connectTimeout := time.Duration(ctxCfg.ConnectTimeoutMS) * time.Millisecond
	if connectTimeout <= 0 {
		connectTimeout = defaultTimeout
	}
	readTimeout := time.Duration(ctxCfg.ReadTimeoutMS) * time.Millisecond
	if readTimeout <= 0 {
		readTimeout = defaultTimeout
	}
	writeTimeout := time.Duration(ctxCfg.WriteTimeoutMS) * time.Millisecond
	if writeTimeout <= 0 {
		writeTimeout = defaultTimeout
	}
	poolSize := ctxCfg.PoolSize
	if poolSize <= 0 {
		poolSize = 4
	}
	maxIdle := ctxCfg.MaxIdle
	if maxIdle <= 0 || maxIdle > poolSize {
		maxIdle = poolSize
	}
	maxRetries := ctxCfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	retryBackoff := time.Duration(ctxCfg.RetryBackoffMS) * time.Millisecond
	if retryBackoff <= 0 {
		retryBackoff = 50 * time.Millisecond
	}
	client := &kvliteClient{
		addr:           ctxCfg.Addr,
		connectTimeout: connectTimeout,
		readTimeout:    readTimeout,
		writeTimeout:   writeTimeout,
		maxRetries:     maxRetries,
		retryBackoff:   retryBackoff,
	}
	client.pool = newKVLiteConnPool(ctxCfg.Addr, poolSize, maxIdle, connectTimeout)
	return client
}

func (k *kvliteClient) Name() string {
	return "kvlite"
}

func (k *kvliteClient) Close() error {
	if k == nil || k.pool == nil {
		return nil
	}
	k.pool.Close()
	return nil
}

func (k *kvliteClient) RGet(ctx context.Context, key string) (string, error) {
	val, err := k.do(ctx, "RGET", true, func(conn *kvliteConn) (any, error) {
		return conn.exec(k.writeTimeout, k.readTimeout, "RGET", key)
	})
	if err != nil {
		return "", err
	}
	str, err := respStringValue(val)
	if err != nil {
		return "", err
	}
	if isNotFoundPayload(str) {
		return "", nil
	}
	return str, nil
}

func (k *kvliteClient) RSet(ctx context.Context, key, value string, ttl time.Duration) error {
	commands := [][]string{{"RSET", key, value}}
	if ttl > 0 {
		commands = append(commands, []string{"PEXPIREAT", key, strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)})
	}
	_, err := k.doBatch(ctx, "RSET", true, commands)
	return err
}

func (k *kvliteClient) Del(ctx context.Context, key string) error {
	if _, err := k.do(ctx, "DEL", true, func(conn *kvliteConn) (any, error) {
		return conn.exec(k.writeTimeout, k.readTimeout, "DEL", key)
	}); err != nil {
		return err
	}
	return nil
}

func (k *kvliteClient) do(ctx context.Context, op string, retryable bool, exec func(*kvliteConn) (any, error)) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := 0
	for {
		conn, err := k.pool.Acquire(ctx)
		if err != nil {
			return nil, wrapKVLiteError(op, err)
		}
		val, execErr := exec(conn)
		if execErr != nil {
			k.pool.Release(conn, false)
			if retryable && attempts < k.maxRetries && isRetryableKVLiteError(execErr) {
				attempts++
				time.Sleep(k.retryBackoff)
				continue
			}
			return nil, wrapKVLiteError(op, execErr)
		}
		k.pool.Release(conn, true)
		return val, nil
	}
}

func (k *kvliteClient) doBatch(ctx context.Context, op string, retryable bool, commands [][]string) ([]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := 0
	for {
		conn, err := k.pool.Acquire(ctx)
		if err != nil {
			return nil, wrapKVLiteError(op, err)
		}
		vals, execErr := conn.execBatch(k.writeTimeout, k.readTimeout, commands)
		if execErr != nil {
			k.pool.Release(conn, false)
			if retryable && attempts < k.maxRetries && isRetryableKVLiteError(execErr) {
				attempts++
				time.Sleep(k.retryBackoff)
				continue
			}
			return nil, wrapKVLiteError(op, execErr)
		}
		k.pool.Release(conn, true)
		return vals, nil
	}
}

func wrapKVLiteError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrKVLiteTimeout) || isTimeoutError(err) {
		return fmt.Errorf("%w: %s %v", ErrKVLiteTimeout, op, err)
	}
	if errors.Is(err, ErrKVLiteConnError) || isConnError(err) {
		return fmt.Errorf("%w: %s %v", ErrKVLiteConnError, op, err)
	}
	return err
}

func isRetryableKVLiteError(err error) bool {
	return errors.Is(err, ErrKVLiteTimeout) || errors.Is(err, ErrKVLiteConnError) || isTimeoutError(err) || isConnError(err)
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")
}

func isConnError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection") || strings.Contains(msg, "closed network connection") || strings.Contains(msg, "broken pipe")
}

type kvliteConn struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
}

func (c *kvliteConn) close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close()
	c.conn = nil
	c.r = nil
	c.w = nil
}

func (c *kvliteConn) exec(writeTimeout, readTimeout time.Duration, args ...string) (any, error) {
	if c == nil || c.conn == nil {
		return nil, ErrKVLiteConnError
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return nil, err
	}
	if err := writeRESPArray(c.w, args); err != nil {
		return nil, err
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return nil, err
	}
	val, err := readRESPValue(c.r)
	if err != nil {
		return nil, err
	}
	return val, nil
}

func (c *kvliteConn) execBatch(writeTimeout, readTimeout time.Duration, commands [][]string) ([]any, error) {
	if c == nil || c.conn == nil {
		return nil, ErrKVLiteConnError
	}
	if len(commands) == 0 {
		return nil, nil
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return nil, err
	}
	for _, args := range commands {
		if err := writeRESPArray(c.w, args); err != nil {
			return nil, err
		}
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return nil, err
	}
	vals := make([]any, 0, len(commands))
	for range commands {
		val, err := readRESPValue(c.r)
		if err != nil {
			return nil, err
		}
		vals = append(vals, val)
	}
	return vals, nil
}

type kvliteConnPool struct {
	addr           string
	maxSize        int
	maxIdle        int
	connectTimeout time.Duration
	mu             sync.Mutex
	active         int
	idle           chan *kvliteConn
	closed         bool
}

func newKVLiteConnPool(addr string, maxSize, maxIdle int, connectTimeout time.Duration) *kvliteConnPool {
	if maxSize <= 0 {
		maxSize = 1
	}
	if maxIdle <= 0 || maxIdle > maxSize {
		maxIdle = maxSize
	}
	if connectTimeout <= 0 {
		connectTimeout = 2 * time.Second
	}
	return &kvliteConnPool{
		addr:           addr,
		maxSize:        maxSize,
		maxIdle:        maxIdle,
		connectTimeout: connectTimeout,
		idle:           make(chan *kvliteConn, maxIdle),
	}
}

func (p *kvliteConnPool) Acquire(ctx context.Context) (*kvliteConn, error) {
	if p == nil {
		return nil, ErrKVLiteConnError
	}
	select {
	case conn := <-p.idle:
		if conn != nil && conn.conn != nil {
			return conn, nil
		}
	default:
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrKVLiteConnError
	}
	if p.active < p.maxSize {
		p.active++
		p.mu.Unlock()
		conn, err := p.dial()
		if err != nil {
			p.mu.Lock()
			p.active--
			p.mu.Unlock()
			return nil, err
		}
		return conn, nil
	}
	idle := p.idle
	p.mu.Unlock()
	waitTimer := time.NewTimer(p.connectTimeout)
	defer waitTimer.Stop()
	select {
	case conn := <-idle:
		if conn != nil && conn.conn != nil {
			return conn, nil
		}
		return nil, ErrKVLiteConnError
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-waitTimer.C:
		return nil, ErrKVLiteTimeout
	}
}

func (p *kvliteConnPool) Release(conn *kvliteConn, keep bool) {
	if p == nil || conn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !keep || conn.conn == nil {
		conn.close()
		if p.active > 0 {
			p.active--
		}
		return
	}
	select {
	case p.idle <- conn:
	default:
		conn.close()
		if p.active > 0 {
			p.active--
		}
	}
}

func (p *kvliteConnPool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()
	if idle == nil {
		return
	}
	for {
		select {
		case conn, ok := <-idle:
			if !ok {
				return
			}
			if conn != nil {
				conn.close()
			}
		default:
			return
		}
	}
}

func (p *kvliteConnPool) dial() (*kvliteConn, error) {
	conn, err := net.DialTimeout("tcp", p.addr, p.connectTimeout)
	if err != nil {
		if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
			return nil, fmt.Errorf("%w: %v", ErrKVLiteTimeout, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrKVLiteConnError, err)
	}
	return &kvliteConn{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn)}, nil
}

func writeRESPArray(w *bufio.Writer, args []string) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return nil
}

func readRESPValue(r *bufio.Reader) (any, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	switch prefix {
	case '+':
		return line, nil
	case '-':
		if strings.Contains(strings.ToUpper(line), "NO EXIST") || strings.Contains(strings.ToLower(line), "nil") {
			return nil, nil
		}
		return nil, errors.New(line)
	case ':':
		return strconv.ParseInt(line, 10, 64)
	case '$':
		n, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if n == -1 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		return nil, errors.New("array response not supported")
	default:
		return strings.TrimSpace(string(prefix) + line), nil
	}
}

func isNotFoundPayload(s string) bool {
	s = strings.TrimSpace(strings.ToUpper(s))
	return s == "NO EXIST" || s == "NIL" || s == "(NIL)"
}

func respStringValue(v any) (string, error) {
	switch val := v.(type) {
	case nil:
		return "", nil
	case string:
		return val, nil
	case []byte:
		return string(val), nil
	case int64:
		return strconv.FormatInt(val, 10), nil
	default:
		return "", fmt.Errorf("unexpected resp value type %T", v)
	}
}
