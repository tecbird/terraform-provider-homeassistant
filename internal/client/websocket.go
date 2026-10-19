package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// WSCommander sends one WebSocket command and waits for its result. Resources depend on this
// interface rather than on WSClient, so they can be tested against fakes.
type WSCommander interface {
	// Command sends `{"id": <n>, "type": typ, ...params}` and decodes the result into result
	// (which may be nil). A `success: false` reply is returned as *WSError.
	Command(ctx context.Context, typ string, params map[string]any, result any) error
}

var _ WSCommander = (*WSClient)(nil)

// WSClient sends commands over one lazily opened `/api/websocket` connection (ADR-0004). It is
// safe for concurrent use.
type WSClient struct {
	cfg  Config
	http *http.Client

	// mu guards conn and dialing. It is not held while dialling.
	mu   sync.Mutex
	conn *wsConn
	// dialing is the dial in progress, if any. Concurrent first commands share it.
	dialing *wsDial
}

// wsDial is one dial that several commands may wait for. done is closed once conn and err are set.
type wsDial struct {
	done chan struct{}
	conn *wsConn
	err  error
	// callerGone means the dialling command's own context ended the dial (cancel, or a deadline
	// of the caller's), so the error says nothing about Home Assistant.
	callerGone bool
}

// newWSClient builds a WSClient. It does not dial.
func newWSClient(cfg Config, httpClient *http.Client) *WSClient {
	return &WSClient{cfg: cfg, http: httpClient}
}

// wsConn is one authenticated connection and the commands waiting on it.
type wsConn struct {
	ws *websocket.Conn

	// writeMu serialises writes. It is taken before mu when assigning an id, so HA sees ids in
	// increasing order.
	writeMu sync.Mutex
	// mu guards nextID, pending, and err. It is never held during I/O, so a slow write does not
	// stall the dispatch of other results.
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan wsMessage
	err     error // set once the connection is dead
}

// wsMessage is the subset of HA's WebSocket messages the client reads.
type wsMessage struct {
	ID      int64           `json:"id"`
	Type    string          `json:"type"`
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"` // auth_invalid reason
}

// Command implements WSCommander. The provider's timeout bounds the whole command, including a
// dial if one is needed. A command that runs into that timeout fails the connection, so a dead
// socket (e.g. a half-open TCP connection) is not reused and the next command dials again.
func (c *WSClient) Command(parent context.Context, typ string, params map[string]any, result any) error {
	ctx := parent
	if c.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, c.cfg.Timeout)
		defer cancel()
	}

	conn, err := c.connection(ctx, parent)
	if err != nil {
		return err
	}

	msg := make(map[string]any, len(params)+2)
	for k, v := range params {
		msg[k] = v
	}
	msg["type"] = typ

	reply, err := conn.roundTrip(ctx, msg)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && parent.Err() == nil {
			conn.fail(fmt.Errorf("no reply to %s within %s", typ, c.cfg.Timeout))
			return fmt.Errorf("%s: no reply within %s: %w", typ, c.cfg.Timeout, err)
		}
		return fmt.Errorf("%s: %w", typ, err)
	}
	if !reply.Success {
		if reply.Error == nil {
			return fmt.Errorf("%s: %w", typ, &WSError{Code: "unknown_error", Message: "command failed without details"})
		}
		return fmt.Errorf("%s: %w", typ, &WSError{Code: reply.Error.Code, Message: reply.Error.Message})
	}
	if result != nil && len(reply.Result) > 0 {
		if err := json.Unmarshal(reply.Result, result); err != nil {
			return fmt.Errorf("%s: decoding result: %w", typ, err)
		}
	}
	return nil
}

// Close closes the connection if one is open. A later command dials again. Only tests use it:
// the provider's connection lives as long as the plugin process.
func (c *WSClient) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.ws.Close(websocket.StatusNormalClosure, "")
}

// connection returns the live connection, dialling and authenticating if there is none. ctx is
// the command's context including the provider timeout; parent is the caller's own context.
// Commands that arrive during a dial wait for it, but no longer than their own ctx allows.
func (c *WSClient) connection(ctx, parent context.Context) (*wsConn, error) {
	for {
		c.mu.Lock()
		if c.conn != nil && c.conn.alive() {
			conn := c.conn
			c.mu.Unlock()
			return conn, nil
		}
		c.conn = nil

		d := c.dialing
		if d == nil {
			d = &wsDial{done: make(chan struct{})}
			c.dialing = d
			c.mu.Unlock()

			d.conn, d.err = c.dial(ctx, parent)
			d.callerGone = d.err != nil && parent.Err() != nil

			c.mu.Lock()
			c.dialing = nil
			if d.err == nil {
				c.conn = d.conn
				go c.readLoop(d.conn) // #nosec G118 -- lives as long as the shared connection, not one command
			}
			close(d.done)
			c.mu.Unlock()
			return d.conn, d.err
		}
		c.mu.Unlock()

		select {
		case <-d.done:
			if d.callerGone {
				continue // that caller gave up; this one dials itself
			}
			if d.err != nil {
				return nil, d.err
			}
			// d.conn may have died since; the next iteration checks.
		case <-ctx.Done():
			if err := parent.Err(); err != nil {
				return nil, fmt.Errorf("waiting for the connection to %s: %w", c.endpoint(), err)
			}
			return nil, fmt.Errorf("%w at %s: no connection within %s", ErrUnreachable, c.endpoint(), c.cfg.Timeout)
		}
	}
}

func (c *WSClient) endpoint() string {
	u := *c.cfg.URL
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path += "/api/websocket"
	return u.String()
}

// dial opens the socket and performs the auth handshake:
// `auth_required` → `auth` → `auth_ok` | `auth_invalid`.
//
// A dial that fails, including one that runs into the provider timeout (a host that is down or a
// firewall that drops packets), is ErrUnreachable (ADR-0011). Only when the caller's own parent
// context ended is the context error returned instead.
func (c *WSClient) dial(ctx, parent context.Context) (*wsConn, error) {
	endpoint := c.endpoint()
	//nolint:bodyclose // websocket.Dial owns resp.Body: "You never need to close resp.Body yourself."
	ws, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: c.http})
	if err != nil {
		if parentErr := parent.Err(); parentErr != nil {
			return nil, fmt.Errorf("dialling %s: %w", endpoint, parentErr)
		}
		return nil, fmt.Errorf("%w at %s: %w", ErrUnreachable, endpoint, err)
	}
	// Registry and Lovelace payloads easily exceed the library's 32 KiB default.
	ws.SetReadLimit(-1)

	if err := handshake(ctx, ws, c.cfg.Token); err != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, "")
		return nil, fmt.Errorf("authenticating to %s: %w", endpoint, err)
	}
	return &wsConn{ws: ws, pending: map[int64]chan wsMessage{}}, nil
}

func handshake(ctx context.Context, ws *websocket.Conn, token string) error {
	var msg wsMessage
	if err := wsjson.Read(ctx, ws, &msg); err != nil {
		return err
	}
	if msg.Type != "auth_required" {
		return fmt.Errorf("expected auth_required, got %q", msg.Type)
	}
	if err := wsjson.Write(ctx, ws, map[string]string{"type": "auth", "access_token": token}); err != nil {
		return err
	}
	if err := wsjson.Read(ctx, ws, &msg); err != nil {
		return err
	}
	switch msg.Type {
	case "auth_ok":
		return nil
	case "auth_invalid":
		return fmt.Errorf("%w: %s", ErrUnauthorized, msg.Message)
	default:
		return fmt.Errorf("expected auth_ok, got %q", msg.Type)
	}
}

// readLoop dispatches results to their waiters until the connection fails, then fails every
// pending command with ErrConnectionLost.
func (c *WSClient) readLoop(conn *wsConn) {
	for {
		var msg wsMessage
		if err := wsjson.Read(context.Background(), conn.ws, &msg); err != nil {
			conn.fail(err)
			c.mu.Lock()
			if c.conn == conn {
				c.conn = nil
			}
			c.mu.Unlock()
			return
		}
		if msg.Type != "result" {
			continue // events: the client holds no subscriptions
		}
		conn.mu.Lock()
		ch, ok := conn.pending[msg.ID]
		delete(conn.pending, msg.ID)
		conn.mu.Unlock()
		if ok {
			ch <- msg
		}
	}
}

func (conn *wsConn) alive() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.err == nil
}

// fail marks the connection dead and releases every waiter.
func (conn *wsConn) fail(cause error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.err != nil {
		return
	}
	conn.err = fmt.Errorf("%w: %w", ErrConnectionLost, cause)
	for id, ch := range conn.pending {
		close(ch)
		delete(conn.pending, id)
	}
	_ = conn.ws.CloseNow()
}

// roundTrip assigns the next id, sends msg, and waits for the matching result.
func (conn *wsConn) roundTrip(ctx context.Context, msg map[string]any) (wsMessage, error) {
	ch := make(chan wsMessage, 1)

	conn.writeMu.Lock()
	conn.mu.Lock()
	if conn.err != nil {
		err := conn.err
		conn.mu.Unlock()
		conn.writeMu.Unlock()
		return wsMessage{}, err
	}
	conn.nextID++
	id := conn.nextID
	msg["id"] = id
	conn.pending[id] = ch
	conn.mu.Unlock()
	err := wsjson.Write(ctx, conn.ws, msg)
	conn.writeMu.Unlock()

	if err != nil {
		// A failed or interrupted write leaves the socket unusable.
		conn.fail(err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return wsMessage{}, ctxErr
		}
		return wsMessage{}, fmt.Errorf("%w: %w", ErrConnectionLost, err)
	}

	select {
	case reply, ok := <-ch:
		if !ok {
			conn.mu.Lock()
			err := conn.err
			conn.mu.Unlock()
			return wsMessage{}, err
		}
		return reply, nil
	case <-ctx.Done():
		conn.mu.Lock()
		delete(conn.pending, id)
		conn.mu.Unlock()
		return wsMessage{}, ctx.Err()
	}
}
