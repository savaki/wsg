wsg
------------------------------

`wsg` - web socket gateway is a go library that simplifies local testing of aws websocket 
gateway lambda functions.

`wsg` leverages the api defined by `github.com/aws/aws-lambda-go`.

Given a function with the signature:

```
func handler(ctx context.Context, request events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error)
``` 

You can run this function locally with wsg via

```
gateway, _ := wsg.Wrap(handler)
go gateway.ListenAndServe(":3000")
defer gateway.Shutdown()
```

In this example, messages can be sent to websocket connections via `http://localhost:3000/connections/{id}` e.g.

```
curl -X POST --data `{"hello":"world"}` http://localhost:3000/connections/abc
``` 