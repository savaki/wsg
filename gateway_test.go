package wsg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"golang.org/x/sync/errgroup"
)

type Driver struct {
	remain int64
}

func (d *Driver) connect(t *testing.T, conn *websocket.Conn) {
	log.Println("driver: connect")

	data := map[string]interface{}{
		"hello": "world",
	}
	err := conn.WriteJSON(data)
	assert.Nil(t, err)
}

func (d *Driver) disconnect(t *testing.T) {
	log.Println("driver: disconnect")
}

func (d *Driver) handle(t *testing.T, conn *websocket.Conn, raw json.RawMessage) error {
	log.Println("driver: handle", string(raw))

	v := atomic.AddInt64(&d.remain, -1)
	m := map[string]interface{}{"count": v}
	if v <= 0 {
		return io.EOF
	}

	return conn.WriteJSON(m)
}

func Client(t *testing.T, uri string, driver *Driver) {
	conn, _, err := websocket.DefaultDialer.Dial(uri, nil)
	assert.Nil(t, err)
	defer conn.Close()

	log.Printf("connected to websocket, %v\n", uri)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deadline := 3 * time.Second
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")

		driver.connect(t, conn)
		defer driver.disconnect(t)
		for {
			if err := conn.SetReadDeadline(time.Now().Add(deadline)); err != nil {
				return err
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				// ok
			}

			var raw json.RawMessage
			if err := conn.ReadJSON(&raw); err != nil {
				return fmt.Errorf("unable to read message: %w", err)
			}

			if err := driver.handle(t, conn, raw); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	})
	err = group.Wait()
	assert.Nil(t, err)
}

func Server(ch chan string, port string) HandlerFunc {
	return func(ctx context.Context, req events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
		switch req.RequestContext.RouteKey {
		case routeKeyConnect:
			log.Println("server: connect")
			return events.APIGatewayProxyResponse{}, nil

		case routeKeyDisconnect:
			log.Println("server: disconnect")
			return events.APIGatewayProxyResponse{}, nil
		}

		data, err := decode(req.Body, req.IsBase64Encoded)
		if err != nil {
			return events.APIGatewayProxyResponse{}, fmt.Errorf("failed to handle request; %w", err)
		}

		log.Printf("%v: [%v] %v\n", req.RequestContext.ConnectionID, req.RequestContext.RouteKey, string(data))
		select {
		case <-ctx.Done():
			return events.APIGatewayProxyResponse{}, ctx.Err()

		case ch <- req.Body:
			uri := fmt.Sprintf("http://localhost:%v/connections/%v", port, req.RequestContext.ConnectionID)
			request, err := http.NewRequest(http.MethodPost, uri, bytes.NewReader(data))
			if err != nil {
				return events.APIGatewayProxyResponse{}, fmt.Errorf("http request failed: %w", err)
			}

			resp, err := http.DefaultClient.Do(request)
			if err != nil {
				return events.APIGatewayProxyResponse{}, fmt.Errorf("http request failed: %w", err)
			}
			defer resp.Body.Close()
			return events.APIGatewayProxyResponse{}, nil
		}
	}
}

func TestLive(t *testing.T) {
	var (
		ch      = make(chan string, 8)
		port    = getEnvOrElse("PORT", "3000")
		mock    = Server(ch, port)
		gateway = Wrap(mock, WithDebug(log.Printf))
		uri     = fmt.Sprintf("ws://localhost:%v", port)
		addr    = ":" + port
	)

	go gateway.ListenAndServe(addr) // listen for callbacks
	defer gateway.Shutdown()

	driver := Driver{remain: 5}
	Client(t, uri, &driver)
	assert.Zero(t, driver.remain)
}

func decode(body string, isBase64Encoded bool) ([]byte, error) {
	if !isBase64Encoded {
		return []byte(body), nil
	}

	data, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("failed to decode body: %w", err)
	}

	return data, nil
}

func getEnvOrElse(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func TestGateway_URL(t *testing.T) {
	fn := func(ctx context.Context, request events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
		return events.APIGatewayProxyResponse{}, nil
	}
	gateway := Wrap(fn)
	go gateway.ListenAndServe(":")
	defer gateway.Shutdown()

	time.Sleep(100 * time.Millisecond)
	raw := gateway.URL("abc")
	if _, err := url.Parse(raw); err != nil {
		t.Fatalf("got %v; want nil", err)
	}
}
