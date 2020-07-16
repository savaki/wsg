package wsg

import (
	"net"
	"net/http"
)

type Options struct {
	debug      func(format string, args ...interface{})
	listener   net.Listener
	middleware []func(h http.Handler) http.Handler
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
