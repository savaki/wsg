package wsg

import "net"

type Options struct {
	debug    func(format string, args ...interface{})
	listener net.Listener
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
