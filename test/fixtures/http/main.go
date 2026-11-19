// http checks gopgext/http's failure modes offline (the victoria_fdw tests
// exercise it against real servers).
package main

import (
	"io"
	"time"

	"gopgext/http"
)

// HttpGet returns the status and body of a GET, or its error.
//
//gopgext:function
func HttpGet(url string) (string, error) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.Status + " " + string(body), err
}

func main() {}
