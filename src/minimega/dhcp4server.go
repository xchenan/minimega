// Example of minimal DHCP server:
package main

import (
	"github.com/krolaw/dhcp4"

	"log"
	"math/rand"
	"minicli"
	"net"
	"time"
)

var dhcp4CLIHandlers = []minicli.Handler{
	{ // dhcp4Server
		HelpShort: "start a dhcpv4 server on a specified ip",
		HelpLong: `
Start a dhcp/dns server on a specified IP. For example,
to start a DHCP server on 192.168.0.1, do:

	dhcp4 ip 192.168.0.1

To specify an server interface, eth0, do the following:

	dhcp4 i eth0 ip 192.168.0.1`,

		Patterns: []string{
			"dhcp4 ip <ipv4 address>",
			"dhcp4 i <interface> ip <ipv4 address>",
		},
		Call: wrapSimpleCLI(cliDhcp4Server),
	},
}

// Example using DHCP with a single network interface device
func cliDhcp4Server(c *minicli.Command, resp *minicli.Response) error {
	log.Printf("string args: %v\n", c.StringArgs)
	return nil
	//	serverIP := net.IP{172, 30, 0, 1}
	//	handler := &DHCPHandler{
	//		ip:            serverIP,
	//		leaseDuration: 2 * time.Hour,
	//		start:         net.IP{172, 30, 0, 2},
	//		leaseRange:    50,
	//		leases:        make(map[int]lease, 10),
	//		options: dhcp4.Options{
	//			dhcp4.OptionSubnetMask:       []byte{255, 255, 240, 0},
	//			dhcp4.OptionRouter:           []byte(serverIP), // Presuming Server is also your router
	//			dhcp4.OptionDomainNameServer: []byte(serverIP), // Presuming Server is also your DNS server
	//		},
	//	}
	//	log.Fatal(dhcp4.ListenAndServe(handler))
	// log.Fatal(dhcp4.ListenAndServeIf("eth0",handler)) // Select interface on multi interface device
}

type lease struct {
	nic    string    // Client's CHAddr
	expiry time.Time // When the lease expires
}

type DHCPHandler struct {
	ip            net.IP        // Server IP to use
	options       dhcp4.Options // Options to send to DHCP Clients
	start         net.IP        // Start of IP range to distribute
	leaseRange    int           // Number of IPs to distribute (starting from start)
	leaseDuration time.Duration // Lease period
	leases        map[int]lease // Map to keep track of leases
}

func (h *DHCPHandler) ServeDHCP(p dhcp4.Packet, msgType dhcp4.MessageType, options dhcp4.Options) (d dhcp4.Packet) {
	switch msgType {

	case dhcp4.Discover:
		free, nic := -1, p.CHAddr().String()
		for i, v := range h.leases { // Find previous lease
			if v.nic == nic {
				free = i
				goto reply
			}
		}
		if free = h.freeLease(); free == -1 {
			return
		}
	reply:
		return dhcp4.ReplyPacket(p, dhcp4.Offer, h.ip, dhcp4.IPAdd(h.start, free), h.leaseDuration,
			h.options.SelectOrderOrAll(options[dhcp4.OptionParameterRequestList]))

	case dhcp4.Request:
		if server, ok := options[dhcp4.OptionServerIdentifier]; ok && !net.IP(server).Equal(h.ip) {
			return nil // Message not for this dhcp server
		}
		reqIP := net.IP(options[dhcp4.OptionRequestedIPAddress])
		if reqIP == nil {
			reqIP = net.IP(p.CIAddr())
		}

		if len(reqIP) == 4 && !reqIP.Equal(net.IPv4zero) {
			if leaseNum := dhcp4.IPRange(h.start, reqIP) - 1; leaseNum >= 0 && leaseNum < h.leaseRange {
				if l, exists := h.leases[leaseNum]; !exists || l.nic == p.CHAddr().String() {
					h.leases[leaseNum] = lease{nic: p.CHAddr().String(), expiry: time.Now().Add(h.leaseDuration)}
					return dhcp4.ReplyPacket(p, dhcp4.ACK, h.ip, reqIP, h.leaseDuration,
						h.options.SelectOrderOrAll(options[dhcp4.OptionParameterRequestList]))
				}
			}
		}
		return dhcp4.ReplyPacket(p, dhcp4.NAK, h.ip, nil, 0, nil)

	case dhcp4.Release, dhcp4.Decline:
		nic := p.CHAddr().String()
		for i, v := range h.leases {
			if v.nic == nic {
				delete(h.leases, i)
				break
			}
		}
	}
	return nil
}

func (h *DHCPHandler) freeLease() int {
	now := time.Now()
	b := rand.Intn(h.leaseRange) // Try random first
	for _, v := range [][]int{[]int{b, h.leaseRange}, []int{0, b}} {
		for i := v[0]; i < v[1]; i++ {
			if l, ok := h.leases[i]; !ok || l.expiry.Before(now) {
				return i
			}
		}
	}
	return -1
}
