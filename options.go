package wsg

import (
	"net"
	"net/http"
)

type Options struct {
	debug      func(format string, args ...interface{})
	listener   net.Listener
	middleware []func(h http.Handler) http.Handler
	onWrite    []func([]byte, error)
}

type Option func(*Options)

func buildOptions(opts ...Option) Options {
	options := Options{}
	for _, opt := range opts {
		opt(&options)
	}
	if options.debug == nil {
		options.debug = func(format string, args ...interface{}) {}
	}
	return options
}

func WithDebug(fn func(format string, args ...interface{})) Option {
	return func(o *Options) {
		o.debug = fn
	}
}

func WithMiddleware(middleware ...func(http.Handler) http.Handler) Option {
	return func(o *Options) {
		o.middleware = append(o.middleware, middleware...)
	}
}

// WithOnWrite allows for custom callback when messages are written to sockets
func WithOnWrite(callback func([]byte, error)) Option {
	return func(o *Options) {
		if callback != nil {
			o.onWrite = append(o.onWrite, callback)
		}
	}
}
