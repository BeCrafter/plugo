// Copyright 2015 Giulio Iotti. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package plugo

import (
	"errors"
	"log"
	"strings"
)

const (
	errorCodeConnFailed = "err-connection-failed"
	errorCodeHttpServe  = "err-http-serve"
)

// Error reported when connection to the external plugin has failed.
type ErrConnectionFailed error

// Error reported when the external plugin cannot start listening for calls.
type ErrHttpServe error

// Error reported when an invalid message is printed by the external plugin.
type ErrInvalidMessage error

// Error reported when the plugin fails to register before the registration
// timeout expires.
type ErrRegistrationTimeout error

func parseError(line string) error {
	parts := strings.SplitN(line, ": ", 2)
	if parts[0] == "" {
		return nil
	}

	err := errors.New(parts[1])

	switch parts[0] {
	case errorCodeConnFailed:
		return ErrConnectionFailed(err)
	case errorCodeHttpServe:
		return ErrHttpServe(err)
	}

	return err
}

// ErrorHandler is the interface used by Plugin to report non-fatal errors and any other
// output from the plugin.
//
// A default implementation is provided and used if none is specified on plugin creation.
type ErrorHandler interface {
	// Error is called whenever a non-fatal error occurs in the plugin subprocess.
	Error(error)
	// Print is called for each line of output received from the plugin subprocess.
	Print(interface{})
}

// Default error handler implementation. Uses the default logging facility from the
// Go standard library.
type DefaultErrorHandler struct{}

// Constructor for default error handler.
func NewDefaultErrorHandler() *DefaultErrorHandler {
	return &DefaultErrorHandler{}
}

// Log via default standard library facility prepending the "error: " string.
func (e *DefaultErrorHandler) Error(err error) {
	log.Print("error: ", err)
}

// Log via default standard library facility.
func (e *DefaultErrorHandler) Print(s interface{}) {
	log.Print(s)
}
