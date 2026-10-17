package main

import (
	"reflect"
	"testing"
)

func TestParseArgs(t *testing.T) {
	defaults := options{Type: "auto", WebPort: 8080}
	with := func(f func(o *options)) options {
		o := defaults
		f(&o)
		return o
	}

	tests := []struct {
		name string
		args []string
		want options
	}{
		{"no args", nil, defaults},
		{"host only", []string{"10.0.0.1"}, with(func(o *options) { o.Host = "10.0.0.1" })},
		{"first positional wins", []string{"a", "b"}, with(func(o *options) { o.Host = "a" })},
		{
			"short flags with values",
			[]string{"10.0.0.1", "-u", "admin", "-p", "s3cret"},
			with(func(o *options) { o.Host, o.User, o.Password = "10.0.0.1", "admin", "s3cret" }),
		},
		{
			"long flags with equals",
			[]string{"--user=admin", "--pass=x", "--type=dell", "--web-port=9000", "h"},
			with(func(o *options) { o.Host, o.User, o.Password, o.Type, o.WebPort = "h", "admin", "x", "dell", 9000 }),
		},
		{
			"-p followed by a flag leaves password empty so it is prompted",
			[]string{"h", "-p", "--no-browser"},
			with(func(o *options) { o.Host, o.NoBrowser = "h", true }),
		},
		{"invalid web port keeps default", []string{"h", "--web-port", "abc"}, with(func(o *options) { o.Host = "h" })},
		{"--port value is consumed, not taken as host", []string{"--port", "623", "h"}, with(func(o *options) { o.Host = "h" })},
		{"help", []string{"-h"}, with(func(o *options) { o.ShowHelp = true })},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseArgs(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}
