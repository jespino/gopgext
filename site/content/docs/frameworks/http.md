---
title: "gopgext/http"
linkTitle: "http"
description: "HTTP(S) requests from SQL functions."
weight: 1
---

API reference: [gopgext/http](/api/http/).

An HTTP(S) client on libcurl (TinyGo has no `net/http` here), with
`net/http`'s client API: `http.Get`, `http.Post`, `Client{Timeout, Header}`,
`Client.Do(&Request{...})`, `Response{StatusCode, Header, Body}`. Bodies
are streamed (read them with `bufio`, `json.Decoder`, `io.ReadAll`), waiting
for the server notices query cancellation, only `http://` and `https://`
URLs are allowed (redirects included), and a request is closed when the
transaction ends. Importing it links the extension with libcurl (install
it with its headers to build).

```go
resp, err := http.Get("https://api.example.com/rates")
if err != nil {
	return err
}
defer resp.Body.Close()
err = json.NewDecoder(resp.Body).Decode(&rates)
```
