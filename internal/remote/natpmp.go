package remote

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"
)

// NATPMPPort is the gateway port NAT-PMP (RFC 6886) listens on.
const NATPMPPort = 5351

// NATPMP is a minimal RFC 6886 client.
type NATPMP struct {
	// Gateway is "ip:port" of the gateway's NAT-PMP service.
	Gateway string
	// Tries bounds retransmissions (RFC: 250ms doubling); default 4.
	Tries int
}

// NewNATPMP returns a client for gateway (port NATPMPPort).
func NewNATPMP(gateway net.IP) *NATPMP {
	return &NATPMP{Gateway: net.JoinHostPort(gateway.String(), fmt.Sprint(NATPMPPort))}
}

// NATPMPError is a non-zero NAT-PMP result code.
type NATPMPError struct{ Code uint16 }

func (e *NATPMPError) Error() string {
	msgs := map[uint16]string{1: "unsupported version", 2: "not authorized/refused", 3: "network failure", 4: "out of resources", 5: "unsupported opcode"}
	return fmt.Sprintf("nat-pmp result %d: %s", e.Code, msgs[e.Code])
}

func (n *NATPMP) call(ctx context.Context, req []byte, respLen int) ([]byte, error) {
	conn, err := net.Dial("udp4", n.Gateway)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	tries := n.Tries
	if tries <= 0 {
		tries = 4
	}
	timeout := 250 * time.Millisecond
	buf := make([]byte, 64)
	for i := 0; i < tries; i++ {
		if _, err := conn.Write(req); err != nil {
			return nil, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		for {
			m, err := conn.Read(buf)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					break
				}
				return nil, err
			}
			if m < respLen || buf[0] != 0 || buf[1] != req[1]+128 {
				continue // stray or malformed packet
			}
			if code := binary.BigEndian.Uint16(buf[2:4]); code != 0 {
				return nil, &NATPMPError{Code: code}
			}
			return append([]byte(nil), buf[:m]...), nil
		}
		timeout *= 2
	}
	return nil, fmt.Errorf("nat-pmp: no response from %s", n.Gateway)
}

// ExternalIP asks the gateway for its public address.
func (n *NATPMP) ExternalIP(ctx context.Context) (net.IP, error) {
	resp, err := n.call(ctx, []byte{0, 0}, 12)
	if err != nil {
		return nil, err
	}
	return net.IPv4(resp[8], resp[9], resp[10], resp[11]).To4(), nil
}

// Map requests a TCP ("tcp") or UDP mapping. lease 0 deletes the mapping.
// It returns the external port and lease the gateway granted.
func (n *NATPMP) Map(ctx context.Context, protocol string, internal, external int, lease time.Duration) (int, time.Duration, error) {
	op := byte(2)
	if strings.EqualFold(protocol, "udp") {
		op = 1
	}
	req := make([]byte, 12)
	req[1] = op
	binary.BigEndian.PutUint16(req[4:6], uint16(internal))
	binary.BigEndian.PutUint16(req[6:8], uint16(external))
	binary.BigEndian.PutUint32(req[8:12], uint32(lease/time.Second))
	resp, err := n.call(ctx, req, 16)
	if err != nil {
		return 0, 0, err
	}
	ext := int(binary.BigEndian.Uint16(resp[10:12]))
	granted := time.Duration(binary.BigEndian.Uint32(resp[12:16])) * time.Second
	return ext, granted, nil
}

// DefaultGateway returns the IPv4 default gateway. On Linux it reads
// /proc/net/route; elsewhere it guesses the ".1" address of the first
// private LAN subnet, which is right for the vast majority of home
// networks. NAT-PMP is only a fallback after UPnP, so a wrong guess just
// means the fallback fails.
func DefaultGateway() (net.IP, error) {
	if runtime.GOOS == "linux" {
		if ip, err := linuxGateway("/proc/net/route"); err == nil {
			return ip, nil
		}
	}
	addrs, err := LANAddresses(LANOptions{})
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		v4 := a.IP.To4()
		if v4 == nil || !v4.IsPrivate() || a.Net == nil {
			continue
		}
		mask := a.Net.Mask
		if len(mask) == net.IPv6len {
			mask = mask[12:]
		}
		gw := v4.Mask(mask)
		if gw == nil {
			continue
		}
		gw = append(net.IP(nil), gw...)
		gw[3] |= 1
		if gw.Equal(v4) {
			continue
		}
		return gw, nil
	}
	return nil, errors.New("remote: default gateway unknown")
}

func linuxGateway(path string) (net.IP, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseProcRoute(bufio.NewScanner(f))
}

func parseProcRoute(sc *bufio.Scanner) (net.IP, error) {
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		b, err := hex.DecodeString(fields[2])
		if err != nil || len(b) != 4 {
			continue
		}
		// /proc/net/route is host byte order (little endian on all Go linux targets that matter).
		ip := net.IPv4(b[3], b[2], b[1], b[0]).To4()
		if !ip.IsUnspecified() {
			return ip, nil
		}
	}
	return nil, errors.New("no default route")
}
