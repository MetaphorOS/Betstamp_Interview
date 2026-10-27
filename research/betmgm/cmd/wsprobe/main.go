// wsprobe tests which request headers BetMGM's SignalR socket needs to upgrade,
// then (optionally) subscribes to topics and logs frames.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
	rs       = "\x1e"
)

func main() {
	aid := flag.String("aid", "ZjNmOGM4OTAtZmVhOS00ZTZiLWE5MDQtNGVjNWM5MDkxNjQ1", "x-bwin-accessId")
	topics := flag.String("topics", "", "comma-separated topics to subscribe after the ablation")
	secs := flag.Int("seconds", 0, "how long to log frames when -topics is set")
	logPath := flag.String("log", "", "JSONL-ish frame log (one SignalR message per line)")
	flag.Parse()
	url := "wss://cds-push.betmgm.ca/ws-1-0?lang=en-us&country=CA&x-bwin-accessId=" + *aid + "&appUpdates=false"

	cases := []struct {
		name string
		hdr  http.Header
	}{
		{"no headers", http.Header{}},
		{"origin only", http.Header{"Origin": {"https://www.betmgm.ca"}}},
		{"chrome UA only", http.Header{"User-Agent": {chromeUA}}},
		{"chrome UA + origin", http.Header{"User-Agent": {chromeUA}, "Origin": {"https://www.betmgm.ca"}}},
	}
	var ok http.Header
	for _, c := range cases {
		d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		conn, resp, err := d.Dial(url, c.hdr)
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		fmt.Printf("%-20s status=%d err=%v\n", c.name, code, err)
		if err == nil {
			conn.Close()
			if ok == nil {
				ok = c.hdr
			}
		}
	}
	if *topics == "" || ok == nil {
		return
	}

	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).Dial(url, ok)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer conn.Close()
	var log *os.File
	if *logPath != "" {
		log, _ = os.Create(*logPath)
		defer log.Close()
	}
	conn.WriteMessage(websocket.TextMessage, []byte(`{"protocol":"json","version":1}`+rs))
	quoted := make([]string, 0)
	for _, t := range strings.Split(*topics, ",") {
		quoted = append(quoted, fmt.Sprintf("%q", t))
	}
	sub := fmt.Sprintf(`{"target":"Subscribe","arguments":[{"topics":[%s]}],"invocationId":"1","type":1}`, strings.Join(quoted, ","))
	conn.WriteMessage(websocket.TextMessage, []byte(sub+rs))

	deadline := time.Now().Add(time.Duration(*secs) * time.Second)
	counts := map[string]int{}
	go func() {
		for time.Now().Before(deadline) {
			time.Sleep(10 * time.Second)
			conn.WriteMessage(websocket.TextMessage, []byte(`{"type":6}`+rs))
		}
	}()
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			fmt.Println("read:", err)
			break
		}
		for _, msg := range strings.Split(string(data), rs) {
			if msg == "" {
				continue
			}
			kind := msg
			if i := strings.Index(msg, `"messageType":"`); i >= 0 {
				kind = "Receive/" + strings.SplitN(msg[i+15:], `"`, 2)[0]
			} else if len(kind) > 60 {
				kind = kind[:60]
			}
			counts[kind]++
			if log != nil {
				fmt.Fprintf(log, "%s\t%s\n", time.Now().Format(time.RFC3339Nano), msg)
			}
		}
	}
	for k, v := range counts {
		fmt.Printf("%6d  %s\n", v, k)
	}
}
