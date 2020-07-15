package wsg

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
)

func Mock(ch chan string, listener net.Listener) HandlerFunc {
	port := Port(listener)

	return func(ctx context.Context, req events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
		fmt.Printf("%v: [%v] %v\n", req.RequestContext.ConnectionID, req.RequestContext.RouteKey, req.Body)
		select {
		case <-ctx.Done():
			return events.APIGatewayProxyResponse{}, ctx.Err()
		case ch <- req.Body:
			uri := fmt.Sprintf("http://localhost:%v/%v", port, req.RequestContext.ConnectionID)
			request := httptest.NewRequest(http.MethodPost, uri, strings.NewReader(req.Body))
			resp, err := http.DefaultClient.Do(request)
			if err != nil {
				return events.APIGatewayProxyResponse{}, err
			}
			defer resp.Body.Close()
			return events.APIGatewayProxyResponse{}, nil
		}
	}
}

func TestLive(t *testing.T) {
	t.SkipNow()
	listener, err := net.Listen("tcp", "0.0.0.0:")
	if err != nil {
		t.Fatalf("got %v; want nil", err)
	}
	defer listener.Close()

	var (
		ch      = make(chan string, 8)
		mock    = Mock(ch, listener)
		gateway = New(mock)
	)

	go gateway.Serve(listener) // listen for callbacks
	time.Sleep(time.Second)
	http.ListenAndServe(":3030", gateway) // listen for websocket connections
}
