// Command dhcp6d is an example DHCPv6 server.  It can only assign a
// single IPv6 address, and is not a complete DHCPv6 server implementation
// by any means.  It is meant to demonstrate usage of package dhcp6.
package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"minicli"
	"net"
	"time"

	"github.com/mdlayher/dhcp6"
	"golang.org/x/net/ipv6"
)

var dhcp6CLIHandlers = []minicli.Handler{
	{ // dhcp6Server
		HelpShort: "start a dhcpv6 server on a specified ip",
		HelpLong: `
Start a dhcp/dns server on a specified IP. For example,
to start a DHCP server which servers IP address dead:beef:d34d:b33f::10, do:

	dhcp6 ip dead:beef:d34d:b33f::10

To specify an server interface, do the following:

	dhcp6 i eth0 ip dead:beef:d34d:b33f::10`,

		Patterns: []string{
			"dhcp6 ip <ipv6 address>",
			"dhcp6 i <interface> ip <ipv6 address>",
		},
		Call: wrapSimpleCLI(cliDhcp6Server),
	},
}

func cliDhcp6Server(c *minicli.Command, resp *minicli.Response) error {
	log.Printf("command: %v\n", c.StringArgs)

	// Only accept a single IPv6 address
	ip := net.ParseIP(c.StringArgs["ipv6"]).To16()
	if ip == nil || ip.To4() != nil {
		return fmt.Errorf("IP is not an IPv6 address")
	}

	// Set a default interface if not specified
	iface, ok := c.StringArgs["interface"]
	if !ok {
		iface = "eth0"
	}

	// Make Handler to assign ip and use handle for requests
	h := &Handler{
		ip:      ip,
		handler: handle,
	}

	// Listen and serve.
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return err
	}

	s := &dhcp6.Server{
		Iface:   ifi,
		Addr:    "[::]:547",
		Handler: h,
		MulticastGroups: []*net.IPAddr{
			dhcp6.AllRelayAgentsAndServersAddr,
			dhcp6.AllServersAddr,
		},
	}
	conn, err := net.ListenPacket("udp6", s.Addr)
	if err != nil {
		return err
	}

	go func(conn net.PacketConn) {
		defer conn.Close()
		err = s.Serve(ipv6.NewPacketConn(conn))
		log.Printf("dhcp server error: %v\n", err)
	}(conn)

	return nil
}

// A Handler is a basic DHCPv6 handler.
type Handler struct {
	ip      net.IP
	handler handler
}

// ServeDHCP is a dhcp6.Handler which invokes an internal handler that
// allows errors to be returned and handled in one place.
func (h *Handler) ServeDHCP(w dhcp6.ResponseSender, r *dhcp6.Request) {
	if err := h.handler(h.ip, w, r); err != nil {
		log.Println(err)
	}
}

// A handler is a DHCPv6 handler function which can assign a single IPv6
// address and also return an error.
type handler func(ip net.IP, w dhcp6.ResponseSender, r *dhcp6.Request) error

// handle is a handler which assigns IPv6 addresses using DHCPv6.
func handle(ip net.IP, w dhcp6.ResponseSender, r *dhcp6.Request) error {
	// Accept only Solicit, Request, or Confirm, since this server
	// does not handle Information Request or other message types
	valid := map[dhcp6.MessageType]struct{}{
		dhcp6.MessageTypeSolicit: struct{}{},
		dhcp6.MessageTypeRequest: struct{}{},
		dhcp6.MessageTypeConfirm: struct{}{},
	}
	if _, ok := valid[r.MessageType]; !ok {
		return nil
	}

	// Make sure client sent a client ID
	duid, ok := r.Options.Get(dhcp6.OptionClientID)
	if !ok {
		return nil
	}

	// Log information about the incoming request.
	log.Printf("[%s] id: %s, type: %d, len: %d, tx: %s",
		hex.EncodeToString(duid),
		r.RemoteAddr,
		r.MessageType,
		r.Length,
		hex.EncodeToString(r.TransactionID[:]),
	)

	// Print out options the client has requested
	if opts, ok, err := r.Options.OptionRequest(); err == nil && ok {
		log.Println("\t- requested:")
		for _, o := range opts {
			log.Printf("\t\t - %s", o)
		}
	}

	// Client must send a IANA to retrieve an IPv6 address
	ianas, ok, err := r.Options.IANA()
	if err != nil {
		return err
	}
	if !ok {
		log.Println("no IANAs provided")
		return nil
	}

	// Only accept one IANA
	if len(ianas) > 1 {
		log.Println("can only handle one IANA")
		return nil
	}
	ia := ianas[0]

	log.Printf("\tIANA: %s (%s, %s), opts: %v",
		hex.EncodeToString(ia.IAID[:]),
		ia.T1,
		ia.T2,
		ia.Options,
	)

	// Instruct client to prefer this server unconditionally
	_ = w.Options().Add(dhcp6.OptionPreference, dhcp6.Preference(255))

	// IANA may already have an IAAddr if an address was already assigned.
	// If not, assign a new one.
	iaaddrs, ok, err := ia.Options.IAAddr()
	if err != nil {
		return err
	}

	// Client did not indicate a previous address, and is soliciting.
	// Advertise a new IPv6 address.
	if !ok && r.MessageType == dhcp6.MessageTypeSolicit {
		return newIAAddr(ia, ip, w, r)
	} else if !ok {
		// Client did not indicate an address and is not soliciting.  Ignore.
		return nil
	}

	// Confirm or renew an existing IPv6 address

	// Must have an IAAddr, but we ignore if more than one is present
	if len(iaaddrs) == 0 {
		return nil
	}
	iaa := iaaddrs[0]

	log.Printf("\t\tIAAddr: %s (%s, %s), opts: %v",
		iaa.IP,
		iaa.PreferredLifetime,
		iaa.ValidLifetime,
		iaa.Options,
	)

	// Add IAAddr inside IANA, add IANA to options
	_ = ia.Options.Add(dhcp6.OptionIAAddr, iaa)
	_ = w.Options().Add(dhcp6.OptionIANA, ia)

	// Send reply to client
	_, err = w.Send(dhcp6.MessageTypeReply)
	return err
}

// newIAAddr creates a IAAddr for a IANA using the specified IPv6 address,
// and advertises it to a client.
func newIAAddr(ia *dhcp6.IANA, ip net.IP, w dhcp6.ResponseSender, r *dhcp6.Request) error {
	// Send IPv6 address with 60 second preferred lifetime,
	// 90 second valid lifetime, no extra options
	iaaddr, err := dhcp6.NewIAAddr(ip, 60*time.Second, 90*time.Second, nil)
	if err != nil {
		return err
	}

	// Add IAAddr inside IANA, add IANA to options
	_ = ia.Options.Add(dhcp6.OptionIAAddr, iaaddr)
	_ = w.Options().Add(dhcp6.OptionIANA, ia)

	// Advertise address to soliciting clients
	log.Printf("advertising IP: %s", ip)
	_, err = w.Send(dhcp6.MessageTypeAdvertise)
	return err
}
