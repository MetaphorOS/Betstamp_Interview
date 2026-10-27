// platformprobe performs one REST read and one WebSocket handshake, without cookies.
// Build this identical source for Windows and Linux to compare environments.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"runtime"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
	const access = "ZjNmOGM4OTAtZmVhOS00ZTZiLWE5MDQtNGVjNWM5MDkxNjQ1"
	out := map[string]any{"at": time.Now().UTC(), "go": runtime.Version(), "os": runtime.GOOS}
	hc := &http.Client{Timeout: 15 * time.Second}
	if r, e := hc.Get("https://api.ipify.org"); e == nil {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 64))
		r.Body.Close()
		out["egress"] = string(b)
	}
	q := url.Values{"x-bwin-accessid": {access}, "lang": {"en-us"}, "country": {"CA"}, "userCountry": {"CA"}, "subdivision": {"CA-Ontario"}, "fixtureTypes": {"Standard"}, "state": {"Latest"}, "offerMapping": {"Filtered"}, "offerCategories": {"Gridable"}, "fixtureCategories": {"Gridable,NonGridable,Other"}, "sportIds": {"11"}, "competitionIds": {"35"}, "isPriceBoost": {"false"}, "statisticsModes": {"None"}, "skip": {"0"}, "take": {"50"}, "sortBy": {"Tags"}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	trace := &httptrace.ClientTrace{GotConn: func(c httptrace.GotConnInfo) { out["restRemote"] = c.Conn.RemoteAddr().String() }}
	req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", "https://www.betmgm.ca/cds-api/bettingoffer/fixtures?"+q.Encode(), nil)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	r, e := hc.Do(req)
	if e != nil {
		out["restError"] = e.Error()
	} else {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		r.Body.Close()
		out["restStatus"] = r.StatusCode
		out["protocol"] = r.Proto
		out["contentType"] = r.Header.Get("Content-Type")
		out["server"] = r.Header.Get("Server")
		out["cfRay"] = r.Header.Get("Cf-Ray")
		out["cfMitigated"] = r.Header.Get("Cf-Mitigated")
		out["retryAfter"] = r.Header.Get("Retry-After")
		out["bytes"] = len(b)
		if r.TLS != nil {
			out["tlsVersion"] = r.TLS.Version
			out["cipher"] = r.TLS.CipherSuite
			out["alpn"] = r.TLS.NegotiatedProtocol
		}
		var doc struct {
			Fixtures   []json.RawMessage `json:"fixtures"`
			TotalCount int               `json:"totalCount"`
		}
		if json.Unmarshal(b, &doc) == nil {
			out["fixtures"] = len(doc.Fixtures)
			out["totalCount"] = doc.TotalCount
		}
	}
	wq := url.Values{"lang": {"en-us"}, "country": {"CA"}, "x-bwin-accessId": {access}, "appUpdates": {"false"}}
	c, wr, we := (&websocket.Dialer{HandshakeTimeout: 15 * time.Second}).Dial("wss://cds-push.betmgm.ca/ws-1-0?"+wq.Encode(), http.Header{"User-Agent": {ua}, "Origin": {"https://www.betmgm.ca"}})
	if wr != nil {
		out["wsStatus"] = wr.StatusCode
	}
	if we != nil {
		out["wsError"] = we.Error()
		if wr != nil && wr.Body != nil {
			wr.Body.Close()
		}
	} else {
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		e = c.WriteMessage(websocket.TextMessage, []byte("{\"protocol\":\"json\",\"version\":1}\x1e"))
		if e == nil {
			_, b, re := c.ReadMessage()
			if re != nil {
				out["hubError"] = re.Error()
			} else {
				out["hubReply"] = string(b)
			}
		} else {
			out["hubError"] = e.Error()
		}
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}
