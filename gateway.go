package wsg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-chi/chi/middleware"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/go-chi/chi"
	"github.com/gorilla/websocket"
	"golang.org/x/sync/errgroup"
)

const (
	routeKeyConnect    = "$connect"
	routeKeyDisconnect = "$disconnect"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // CheckOrigin allows all origins
} // use default options

type StopFunc func()

type HandlerFunc func(ctx context.Context, request events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error)

type Gateway struct {
	handle       HandlerFunc
	id           int64
	debug        func(string, ...interface{})
	middleware   []func(http.Handler) http.Handler
	onWriteFuncs []func([]byte, error)

	mutex    sync.Mutex
	listener net.Listener
	server   *http.Server
	streams  map[string]chan []byte
}

func Wrap(handler HandlerFunc, opts ...Option) *Gateway {
	options := buildOptions(opts...)
	return &Gateway{
		handle:       handler,
		streams:      map[string]chan []byte{},
		debug:        options.debug,
		middleware:   options.middleware,
		onWriteFuncs: options.onWrite,
	}
}

func (g *Gateway) connect(ctx context.Context, connectionID string) {
	req := makeProxyRequest(connectionID, routeKeyConnect, nil)
	if _, err := g.handle(ctx, req); err != nil {
		g.debug("connect failed: %w", err)
	}
}

func (g *Gateway) disconnect(ctx context.Context, connectionID string) {
	req := makeProxyRequest(connectionID, routeKeyDisconnect, nil)
	if _, err := g.handle(ctx, req); err != nil {
		g.debug("disconnect failed: %w", err)
	}
}

func (g *Gateway) handleCallback(w http.ResponseWriter, req *http.Request) {
	id := chi.URLParam(req, "id")

	g.mutex.Lock()
	ch, ok := g.streams[id]
	g.mutex.Unlock()

	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if ok {
		ch <- data
	}
}

func (g *Gateway) handleWebsocket(w http.ResponseWriter, req *http.Request) {
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		g.debug("failed to upgrade websocket conn: %v", err)
		http.Error(w, fmt.Errorf("failed to upgrade http conn: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	var (
		ctx          = req.Context()
		connectionID = g.nextID()
		ch           = make(chan []byte)
	)

	g.mutex.Lock()
	g.streams[connectionID] = ch
	g.mutex.Unlock()
	defer func() {
		g.mutex.Lock()
		delete(g.streams, connectionID)
		g.mutex.Unlock()
	}()

	g.connect(ctx, connectionID)
	defer g.disconnect(ctx, connectionID)

	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		for {
			var raw json.RawMessage
			if err := conn.ReadJSON(&raw); err != nil {
				var ce *websocket.CloseError
				if ok := errors.As(err, &ce); ok && (ce.Code == websocket.CloseAbnormalClosure || ce.Code == websocket.CloseGoingAway) {
					return nil
				}
				return fmt.Errorf("failed read json from conn, %v: %w", connectionID, err)
			}

			pr := makeProxyRequest(connectionID, "default", raw)
			if _, err := g.handle(ctx, pr); err != nil {
				g.debug("failed to handle request: %w", err)
				continue
			}
		}
	})
	group.Go(func() error {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()

			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return fmt.Errorf("ping failed: %w", err)
				}

			case v := <-ch:
				if err := conn.WriteMessage(websocket.TextMessage, v); err != nil {
					g.onWrite(v, err)
					return fmt.Errorf("failed to write json to client: %w", err)
				}
				g.onWrite(v, nil)
			}
		}
	})
	if err := group.Wait(); err != nil {
		g.debug(err.Error())
	}
}

func (g *Gateway) onWrite(data []byte, err error) {
	for _, fn := range g.onWriteFuncs {
		fn(data, err)
	}
}

func (g *Gateway) nextID() string {
	v := atomic.AddInt64(&g.id, 1)
	return strconv.FormatInt(v, 10)
}

func (g *Gateway) ListenAndServe(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("unable to create listener: %w", err)
	}
	defer listener.Close()

	return g.Serve(listener)
}

func (g *Gateway) Serve(listener net.Listener) error {
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.Use(g.middleware...)
	router.Post("/connections/{id}", g.handleCallback)
	router.NotFound(g.handleWebsocket)

	server := &http.Server{
		Handler: router,
	}

	g.mutex.Lock()
	ok := g.server == nil
	if ok {
		g.listener = listener
		g.server = server
	}
	g.mutex.Unlock()
	if !ok {
		return fmt.Errorf("gateways cannot be reused")
	}

	return server.Serve(listener)
}

func (g *Gateway) Shutdown() error {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	if g.server == nil {
		return nil
	}
	return g.server.Shutdown(context.Background())
}

func (g *Gateway) URL(connectionID string) string {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	if g.listener == nil {
		return ""
	}

	return fmt.Sprintf("http://localhost:%v/connections/%v", Port(g.listener), connectionID)
}

func makeProxyRequest(connectionID, routeKey string, raw json.RawMessage) events.APIGatewayWebsocketProxyRequest {
	return events.APIGatewayWebsocketProxyRequest{
		Body:            base64.StdEncoding.EncodeToString(raw),
		IsBase64Encoded: true,
		RequestContext: events.APIGatewayWebsocketProxyRequestContext{
			ConnectionID: connectionID,
			RouteKey:     routeKey,
		},
	}
}

func Port(listener net.Listener) string {
	addr := listener.Addr().String()
	parts := strings.Split(addr, ":")
	return parts[len(parts)-1]
}
