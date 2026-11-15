// Package testhttp gives tests HTTP clients of their own.
package testhttp

import "net/http"

// Client is an HTTP client of its own transport, without kept-alive
// connections: on the shared default transport, another test's server
// Close (CloseIdleConnections) breaks a request in flight.
func Client() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
}
