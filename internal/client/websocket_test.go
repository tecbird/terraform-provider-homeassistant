package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const testToken = "secret"

// fakeHA is an in-process Home Assistant WebSocket endpoint. It performs the real auth handshake
// and then hands each connection to session.
type fakeHA struct {
	t      *testing.T
	srv    *httptest.Server
	dials  atomic.Int32
	accept func(dial int32, s *fakeSession)
}

// fakeSession is one authenticated connection, seen from the server side.
type fakeSession struct {
	ctx context.Context
	ws  *websocket.Conn
}

func newFakeHA(t *testing.T, session func(dial int32, s *fakeSession)) *fakeHA {
	t.Helper()
	f := &fakeHA{t: t, accept: session}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHA) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/websocket" {
		http.NotFound(w, r)
		return
	}
	dial := f.dials.Add(1)
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		f.t.Errorf("accept: %v", err)
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ctx := r.Context()

	if err := wsjson.Write(ctx, ws, map[string]any{"type": "auth_required", "ha_version": "2026.9.0"}); err != nil {
		return
	}
	var auth map[string]any
	if err := wsjson.Read(ctx, ws, &auth); err != nil {
		return
	}
	if auth["type"] != "auth" || auth["access_token"] != testToken {
		_ = wsjson.Write(ctx, ws, map[string]any{"type": "auth_invalid", "message": "Invalid access token or password"})
		return
	}
	if err := wsjson.Write(ctx, ws, map[string]any{"type": "auth_ok", "ha_version": "2026.9.0"}); err != nil {
		return
	}
	f.accept(dial, &fakeSession{ctx: ctx, ws: ws})
}

func (f *fakeHA) client(t *testing.T, token string) *WSClient {
	t.Helper()
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := newWSClient(Config{URL: u, Token: token, Timeout: 5 * time.Second}, f.srv.Client())
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// read returns the next command, or nil when the client has gone.
func (s *fakeSession) read() map[string]any {
	var msg map[string]any
	if err := wsjson.Read(s.ctx, s.ws, &msg); err != nil {
		return nil
	}
	return msg
}

func (s *fakeSession) reply(cmd map[string]any, result any) {
	_ = wsjson.Write(s.ctx, s.ws, map[string]any{"id": cmd["id"], "type": "result", "success": true, "result": result})
}

func (s *fakeSession) fail(cmd map[string]any, code, message string) {
	_ = wsjson.Write(s.ctx, s.ws, map[string]any{
		"id": cmd["id"], "type": "result", "success": false,
		"error": map[string]any{"code": code, "message": message},
	})
}

// echo answers every command with its own params until the client goes away.
func echo(_ int32, s *fakeSession) {
	for cmd := s.read(); cmd != nil; cmd = s.read() {
		s.reply(cmd, cmd)
	}
}

func TestWSAuthOK(t *testing.T) {
	f := newFakeHA(t, echo)
	c := f.client(t, testToken)

	var got map[string]any
	if err := c.Command(context.Background(), "config/label_registry/list", map[string]any{"x": "y"}, &got); err != nil {
		t.Fatalf("Command: %v", err)
	}
	if got["type"] != "config/label_registry/list" || got["x"] != "y" || got["id"] != float64(1) {
		t.Errorf("server saw %v, want type, params, and id 1", got)
	}

	if err := c.Command(context.Background(), "ping", nil, &got); err != nil {
		t.Fatalf("second Command: %v", err)
	}
	if got["id"] != float64(2) {
		t.Errorf("second id = %v, want 2", got["id"])
	}
	if n := f.dials.Load(); n != 1 {
		t.Errorf("dials = %d, want 1", n)
	}
}

func TestWSAuthInvalid(t *testing.T) {
	f := newFakeHA(t, echo)
	c := f.client(t, "wrong")

	err := c.Command(context.Background(), "ping", nil, nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestWSErrorResult(t *testing.T) {
	f := newFakeHA(t, func(_ int32, s *fakeSession) {
		for cmd := s.read(); cmd != nil; cmd = s.read() {
			s.fail(cmd, "not_found", "Label ID doesn't exist")
		}
	})
	c := f.client(t, testToken)

	err := c.Command(context.Background(), "config/label_registry/delete", map[string]any{"label_id": "x"}, nil)
	var wsErr *WSError
	if !errors.As(err, &wsErr) {
		t.Fatalf("err = %v, want *WSError", err)
	}
	if wsErr.Code != "not_found" || wsErr.Message != "Label ID doesn't exist" {
		t.Errorf("WSError = %+v", wsErr)
	}
}

func TestWSConcurrentOutOfOrder(t *testing.T) {
	const n = 5
	f := newFakeHA(t, func(_ int32, s *fakeSession) {
		// Collect all commands first, then answer them last-in, first-out.
		var cmds []map[string]any
		for len(cmds) < n {
			cmd := s.read()
			if cmd == nil {
				return
			}
			cmds = append(cmds, cmd)
		}
		for i := len(cmds) - 1; i >= 0; i-- {
			s.reply(cmds[i], map[string]any{"n": cmds[i]["n"]})
		}
		echo(0, s)
	})
	c := f.client(t, testToken)

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			var got struct{ N int }
			if err := c.Command(context.Background(), "echo", map[string]any{"n": i}, &got); err != nil {
				t.Errorf("command %d: %v", i, err)
				return
			}
			if got.N != i {
				t.Errorf("command %d got the answer for %d", i, got.N)
			}
		})
	}
	wg.Wait()
	if d := f.dials.Load(); d != 1 {
		t.Errorf("dials = %d, want 1", d)
	}
}

func TestWSDropThenRedial(t *testing.T) {
	f := newFakeHA(t, func(dial int32, s *fakeSession) {
		if dial == 1 {
			s.read()
			_ = s.ws.CloseNow() // drop with the command in flight
			return
		}
		echo(dial, s)
	})
	c := f.client(t, testToken)

	err := c.Command(context.Background(), "ping", nil, nil)
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("first err = %v, want ErrConnectionLost", err)
	}

	var got map[string]any
	if err := c.Command(context.Background(), "ping", nil, &got); err != nil {
		t.Fatalf("after redial: %v", err)
	}
	if got["id"] != float64(1) {
		t.Errorf("id after redial = %v, want 1 (fresh connection)", got["id"])
	}
	if d := f.dials.Load(); d != 2 {
		t.Errorf("dials = %d, want 2", d)
	}
}

func TestWSContextCancel(t *testing.T) {
	received := make(chan struct{})
	f := newFakeHA(t, func(_ int32, s *fakeSession) {
		slow := s.read() // never answered until the next command arrives
		if slow == nil {
			return
		}
		close(received)
		next := s.read()
		if next == nil {
			return
		}
		s.reply(slow, map[string]any{"late": true}) // must be dropped by the client
		s.reply(next, next)
		echo(0, s)
	})
	c := f.client(t, testToken)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Command(ctx, "slow", nil, nil) }()
	<-received
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Command did not return after cancel")
	}

	// The connection survives a cancelled command, and the late reply reaches no one.
	var got map[string]any
	if err := c.Command(context.Background(), "ping", nil, &got); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
	if got["type"] != "ping" {
		t.Errorf("got %v, want the ping echo", got)
	}
	if d := f.dials.Load(); d != 1 {
		t.Errorf("dials = %d, want 1", d)
	}
}

// A caller's cancel that lands while its command is being written must not close the shared
// connection. coder/websocket closes the connection when the write's ctx ends, and only stops
// watching it after the frame is out, so a cancel just after the server got the command used to
// kill the connection (TestWSContextCancel flaked on it). write with an already-cancelled ctx hits
// that window every time; roundTrip would not get that far (TestWSCancelledCommandIsNotSent).
func TestWSCancelDuringWriteKeepsConnection(t *testing.T) {
	f := newFakeHA(t, echo)
	c := f.client(t, testToken)
	if err := c.Command(context.Background(), "ping", nil, nil); err != nil {
		t.Fatal(err)
	}
	conn, err := c.connection(context.Background(), context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cancel()
	if err := conn.write(ctx, map[string]any{"id": 999, "type": "cancelled"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	var got map[string]any
	if err := c.Command(context.Background(), "ping", nil, &got); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
	if !conn.alive() {
		t.Error("the connection was closed")
	}
	if d := f.dials.Load(); d != 1 {
		t.Errorf("dials = %d, want 1", d)
	}
}

// A command cancelled while it waits for its turn to write is not sent: a create that HA carried
// out would be lost to Tofu, which only sees context.Canceled.
func TestWSCancelledCommandIsNotSent(t *testing.T) {
	seen := make(chan string, 4)
	f := newFakeHA(t, func(_ int32, s *fakeSession) {
		for cmd := s.read(); cmd != nil; cmd = s.read() {
			seen <- cmd["type"].(string)
			s.reply(cmd, cmd)
		}
	})
	c := f.client(t, testToken)
	conn, err := c.connection(context.Background(), context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn.writeMu.Lock() // another command is being written
	done := make(chan error, 1)
	go func() {
		_, err := conn.roundTrip(ctx, map[string]any{"type": "create"})
		done <- err
	}()
	cancel()
	conn.writeMu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if err := c.Command(context.Background(), "ping", nil, nil); err != nil {
		t.Fatal(err)
	}
	if typ := <-seen; typ != "ping" {
		t.Errorf("server got %q first, want only ping", typ)
	}
}

// A reply that is already there when the command is cancelled is returned: the command ran in HA,
// and its result (e.g. the ID of a created object) must not be dropped. select would pick either at
// random, so this runs many times.
func TestWSReplyBeforeCancelIsReturned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range 200 {
		ch := make(chan wsMessage, 1)
		conn := &wsConn{pending: map[int64]chan wsMessage{1: ch}}
		ch <- wsMessage{ID: 1, Success: true}

		reply, err := conn.await(ctx, 1, ch)

		if err != nil || !reply.Success {
			t.Fatalf("run %d: reply = %+v, err = %v, want the reply", i, reply, err)
		}
	}
}

func TestWSTimeout(t *testing.T) {
	f := newFakeHA(t, func(_ int32, s *fakeSession) {
		for s.read() != nil { // never answer
		}
	})
	c := f.client(t, testToken)
	c.cfg.Timeout = 50 * time.Millisecond

	err := c.Command(context.Background(), "slow", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

// A command that runs into the provider timeout fails the connection: a half-open socket must
// not swallow every later command (ADR-0004: the next command dials again).
func TestWSTimeoutThenRedial(t *testing.T) {
	f := newFakeHA(t, func(dial int32, s *fakeSession) {
		if dial == 1 {
			for s.read() != nil { // never answer, like a half-open connection
			}
			return
		}
		echo(dial, s)
	})
	c := f.client(t, testToken)
	c.cfg.Timeout = 100 * time.Millisecond

	if err := c.Command(context.Background(), "slow", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first err = %v, want context.DeadlineExceeded", err)
	}

	c.cfg.Timeout = 5 * time.Second
	var got map[string]any
	if err := c.Command(context.Background(), "ping", nil, &got); err != nil {
		t.Fatalf("after timeout: %v", err)
	}
	if got["id"] != float64(1) {
		t.Errorf("id after redial = %v, want 1 (fresh connection)", got["id"])
	}
	if d := f.dials.Load(); d != 2 {
		t.Errorf("dials = %d, want 2", d)
	}
}

// blackhole accepts TCP connections and never answers, like a firewalled host that lets the
// connection through, or an HTTP upgrade that hangs.
func blackhole(t *testing.T) *url.URL {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	return &url.URL{Scheme: "http", Host: l.Addr().String()}
}

// A dial that runs into the provider timeout is ErrUnreachable, so the diagnostic carries the
// ADR-0011 hint. Concurrent first commands share that one dial and all get the same error.
func TestWSDialTimeoutIsUnreachable(t *testing.T) {
	c := newWSClient(Config{URL: blackhole(t), Token: testToken, Timeout: 200 * time.Millisecond}, http.DefaultClient)

	const n = 5
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { errs[i] = c.Command(context.Background(), "ping", nil, nil) })
	}
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, ErrUnreachable) {
			t.Errorf("command %d: err = %v, want ErrUnreachable", i, err)
			continue
		}
		if !strings.Contains(ErrorDetail(err), "-refresh=false") {
			t.Errorf("command %d: detail %q lacks the -refresh=false hint", i, ErrorDetail(err))
		}
	}
}

// A command that arrives during another command's dial waits for it, but returns as soon as its
// own context is cancelled.
func TestWSWaiterRespectsContextDuringDial(t *testing.T) {
	f := newFakeHA(t, echo)
	arrived := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(arrived) })
		<-release
		f.serve(w, r)
	})
	c := f.client(t, testToken)

	first := make(chan error, 1)
	go func() { first <- c.Command(context.Background(), "ping", nil, nil) }()
	<-arrived

	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { second <- c.Command(ctx, "ping", nil, nil) }()
	cancel()
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiter err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not return after cancel while the dial hung")
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatalf("dialling command: %v", err)
	}
	if d := f.dials.Load(); d != 1 {
		t.Errorf("dials = %d, want 1", d)
	}
}

func TestWSUnreachable(t *testing.T) {
	f := newFakeHA(t, echo)
	c := f.client(t, testToken)
	f.srv.Close()

	err := c.Command(context.Background(), "ping", nil, nil)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}

// A plan that only reads REST data sources builds the client but never sends a WebSocket
// command, so no socket may be opened.
func TestNewDoesNotDial(t *testing.T) {
	f := newFakeHA(t, echo)
	u, _ := url.Parse(f.srv.URL)
	ha := New(Config{URL: u, Token: testToken, Timeout: time.Second})

	if d := f.dials.Load(); d != 0 {
		t.Fatalf("dials after New = %d, want 0", d)
	}
	if err := ha.WS.Command(context.Background(), "ping", nil, nil); err != nil {
		t.Fatalf("Command: %v", err)
	}
	if d := f.dials.Load(); d != 1 {
		t.Errorf("dials after first command = %d, want 1", d)
	}
	_ = ha.WS.(*WSClient).Close()
}
