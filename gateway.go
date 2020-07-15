package wsg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aws/aws-lambda-go/events"
	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/gorilla/websocket"
	"golang.org/x/sync/errgroup"
)

var upgrader = websocket.Upgrader{} // use default options

type HandlerFunc func(ctx context.Context, request events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error)

type Gateway struct {
	handle HandlerFunc
	id     int64
	debug  func(string, ...interface{})

	mutex   sync.Mutex
	streams map[string]chan []byte
}

func New(handler HandlerFunc) *Gateway {
	return &Gateway{
		handle:  handler,
		streams: map[string]chan []byte{},
		debug: func(format string, args ...interface{}) {
			format = strings.TrimRight(format, "/") + "\n"
			fmt.Printf(format, args...)
		},
	}
}

func (g *Gateway) connect(ctx context.Context, connectionID string) {
	req := makeProxyRequest(connectionID, "$connect", nil)
	if _, err := g.handle(ctx, req); err != nil {
		g.debug("disconnect failed: %w", err)
	}
}

func (g *Gateway) disconnect(ctx context.Context, connectionID string) {
	req := makeProxyRequest(connectionID, "$disconnect", nil)
	if _, err := g.handle(ctx, req); err != nil {
		g.debug("disconnect failed: %w", err)
	}
}

func (g *Gateway) nextID() string {
	v := atomic.AddInt64(&g.id, 1)
	return strconv.FormatInt(v, 10)
}

func (g *Gateway) Serve(listener net.Listener) {
	router := chi.NewRouter()
	router.Use(middleware.Logger)
	router.Post("/{id}", func(w http.ResponseWriter, req *http.Request) {
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
	})

	server := &http.Server{
		Handler: router,
	}

	_ = server.Serve(listener)
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
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
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case v := <-ch:
				if err := conn.WriteMessage(websocket.TextMessage, v); err != nil {
					return fmt.Errorf("failed to write json to client: %w", err)
				}
			}
		}
	})
	if err := group.Wait(); err != nil {
		g.debug(err.Error())
	}
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
