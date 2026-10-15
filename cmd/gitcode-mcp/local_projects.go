package main

import "net"

func localMCPLoopback(bind string) bool {
	if bind == "" {
		return true
	} // existing transport default is loopback.
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
