// Package clocksync estimates this machine's clock offset with one SNTP
// exchange. Book-to-us latency subtracts a book's publish timestamp from our
// receive time, so a local clock that is a few hundred ms off would otherwise
// show up as latency.
package clocksync

import (
	"encoding/binary"
	"errors"
	"net"
	"time"
)

// ntpEpoch is 1900-01-01, the NTP era-0 origin.
var ntpEpoch = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)

func fromNTP(b []byte) time.Time {
	sec := binary.BigEndian.Uint32(b[0:4])
	frac := binary.BigEndian.Uint32(b[4:8])
	return ntpEpoch.Add(time.Duration(sec)*time.Second + time.Duration((uint64(frac)*1e9)>>32))
}

// Offset returns how far the local clock is BEHIND the server (server - local)
// and the round trip. A local clock that runs ahead gives a negative offset.
func Offset(server string, timeout time.Duration) (offset, rtt time.Duration, err error) {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(server, "123"), timeout)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	req := make([]byte, 48)
	req[0] = 0x23 // LI=0, VN=4, Mode=3 (client)
	t1 := time.Now()
	if _, err := conn.Write(req); err != nil {
		return 0, 0, err
	}
	resp := make([]byte, 48)
	n, err := conn.Read(resp)
	t4 := time.Now()
	if err != nil {
		return 0, 0, err
	}
	if n < 48 || resp[1] == 0 {
		return 0, 0, errors.New("clocksync: bad SNTP reply")
	}
	t2, t3 := fromNTP(resp[32:40]), fromNTP(resp[40:48])
	offset = (t2.Sub(t1) + t3.Sub(t4)) / 2
	rtt = t4.Sub(t1) - t3.Sub(t2)
	return offset, rtt, nil
}

// Best takes several samples and keeps the one with the shortest round trip.
func Best(server string, samples int, timeout time.Duration) (offset, rtt time.Duration, err error) {
	rtt = -1
	for range samples {
		o, r, e := Offset(server, timeout)
		if e != nil {
			err = e
			continue
		}
		if rtt < 0 || r < rtt {
			offset, rtt, err = o, r, nil
		}
	}
	if rtt < 0 {
		return 0, 0, err
	}
	return offset, rtt, nil
}
