package jellyfin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// DiscoveryPort is where Jellyfin and Emby apps broadcast "who is
// JellyfinServer?" / "who is EmbyServer?" to find servers on the LAN.
const DiscoveryPort = 7359

// ServeDiscovery answers LAN discovery broadcasts until ctx ends, so the
// apps' "add server" screens list Ether without typing an address. It
// replies only to the two documented probes and tells the asker an address
// on the network it asked from.
func ServeDiscovery(ctx context.Context, bind string, httpPort int, id, name string) error {
	pc, err := net.ListenPacket("udp4", net.JoinHostPort(bind, strconv.Itoa(DiscoveryPort)))
	if err != nil {
		return err
	}
	slog.Info("Jellyfin and Emby LAN discovery listening", "addr", pc.LocalAddr().String())
	go func() { <-ctx.Done(); _ = pc.Close() }()
	buf := make([]byte, 512)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		probe := strings.ToLower(strings.TrimSpace(string(buf[:n])))
		if probe != "who is jellyfinserver?" && probe != "who is embyserver?" {
			continue
		}
		udp, ok := from.(*net.UDPAddr)
		if !ok || !isLocal(addrOf(udp)) {
			continue
		}
		host := localIPToward(udp)
		if host == "" {
			continue
		}
		reply, _ := json.Marshal(map[string]any{
			"Address": "http://" + net.JoinHostPort(host, strconv.Itoa(httpPort)),
			"Id":      id, "Name": name, "EndpointAddress": nil,
		})
		_ = pc.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = pc.WriteTo(reply, from)
		slog.Info("answered a Jellyfin or Emby app's server search", "from", from.String(), "address", host)
	}
}

// localIPToward is this machine's address on the route to peer.
func localIPToward(peer *net.UDPAddr) string {
	c, err := net.DialUDP("udp4", nil, peer)
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}
