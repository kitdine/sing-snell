package snellv6

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type udpDestinationHandler struct {
	got   chan udpDestinationResult
	count int
}
type udpDestinationResult struct {
	metadata, packet M.Socksaddr
	payload          string
	err              error
}

func (h *udpDestinationHandler) NewConnectionEx(context.Context, net.Conn, M.Socksaddr, M.Socksaddr, N.CloseHandlerFunc) {
	panic("unexpected TCP")
}

func (h *udpDestinationHandler) NewPacketConnectionEx(_ context.Context, conn N.PacketConn, _ M.Socksaddr, destination M.Socksaddr, _ N.CloseHandlerFunc) {
	for i := 0; i < h.count; i++ {
		b := buf.NewPacket()
		packet, err := conn.ReadPacket(b)
		h.got <- udpDestinationResult{destination, packet, string(b.Bytes()), err}
		b.Release()
	}
}

func TestUDPFirstPacketDestination(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeDefault, ModeUnshaped, ModeUnsafeRaw} {
		for _, host := range []string{"ss.epdg.epc.geo.mnc260.mcc310.pub.3gppnetwork.org", "208.54.32.35", "2001:db8::35"} {
			for _, multi := range []bool{false, true} {
				t.Run(fmt.Sprintf("mode=%d/%s/multi=%t", mode, host, multi), func(t *testing.T) {
					t.Parallel()
					handler := &udpDestinationHandler{got: make(chan udpDestinationResult, 3), count: 3}
					options := ServerOptions{PSK: []byte("test-psk-123456789"), Mode: mode, Handler: handler}
					clientOptions := ClientOptions{PSK: options.PSK, Mode: options.Mode}
					var serve func(context.Context, net.Conn, M.Socksaddr, N.CloseHandlerFunc) error
					if multi {
						s, err := NewMultiService[int](options)
						if err != nil {
							t.Fatal(err)
						}
						if err = s.UpdateUsers([]int{1}, [][]byte{[]byte("test-user")}); err != nil {
							t.Fatal(err)
						}
						clientOptions.UserKey = []byte("test-user")
						serve = s.NewConnection
					} else {
						s, err := NewService(options)
						if err != nil {
							t.Fatal(err)
						}
						serve = s.NewConnection
					}
					client, err := NewClient(clientOptions)
					if err != nil {
						t.Fatal(err)
					}
					left, right := net.Pipe()
					defer left.Close()
					defer right.Close()
					deadline := time.Now().Add(3 * time.Second)
					left.SetDeadline(deadline)
					right.SetDeadline(deadline)
					serverErr := make(chan error, 1)
					go func() { serverErr <- serve(context.Background(), left, M.Socksaddr{}, nil) }()
					packetConn := &clientPacketConn{Conn: right, client: client}
					destination := M.ParseSocksaddrHostPort(host, 4500)
					destinations := []M.Socksaddr{destination, M.ParseSocksaddrHostPort(host, 500), M.ParseSocksaddrHostPort("second-domain.invalid", 4500)}
					writeErr := make(chan error, 1)
					go func() {
						for _, target := range destinations {
							b := buf.NewSize(1024)
							b.Resize(512, 0)
							b.WriteString("ike-test-payload")
							if err := packetConn.WritePacket(b, target); err != nil {
								writeErr <- err
								return
							}
						}
						writeErr <- nil
					}()
					if _, err = packetConn.readReply(); err != nil {
						t.Fatal(err)
					}
					if err = <-writeErr; err != nil {
						t.Fatal(err)
					}
					for _, target := range destinations {
						select {
						case got := <-handler.got:
							if got.err != nil {
								t.Fatal(got.err)
							}
							if got.metadata != destination {
								t.Errorf("routing metadata lost destination: got %v, want %v", got.metadata, destination)
							}
							if got.packet != target || got.payload != "ike-test-payload" {
								t.Errorf("first packet corrupted: %+v", got)
							}
						case <-time.After(4 * time.Second):
							t.Fatal("handler timeout")
						}
					}
					if err = <-serverErr; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

// This demonstrates why an unresolved per-packet FQDN must never reach a raw
// UDP socket: UDPAddr drops Fqdn, and Linux interprets the unspecified target
// as loopback. No external traffic or real DNS lookup is used.
func TestUnresolvedDomainBecomesLoopback(t *testing.T) {
	t.Parallel()
	sink, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.ParseIP("::1")})
	if err != nil {
		t.Skip(err)
	}
	defer sink.Close()
	source, err := net.ListenPacket("udp6", "[::]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination := M.ParseSocksaddrHostPort("not-a-real-host.invalid", uint16(sink.LocalAddr().(*net.UDPAddr).Port))
	if destination.UDPAddr().IP != nil {
		t.Fatal("expected unresolved IP")
	}
	if normalErr := bufio.NewPacketConn(source).WritePacket(buf.As([]byte("must-not-arrive")), destination); normalErr == nil {
		t.Fatal("ordinary UDP writer unexpectedly accepted unresolved domain")
	} else {
		t.Logf("ordinary UDP writer correctly rejected unresolved FQDN: %v", normalErr)
	}
	writer, ok := bufio.CreatePacketBatchWriter(bufio.NewPacketConn(source))
	if !ok {
		t.Skip("no syscall batch writer")
	}
	if err = writer.WritePacketBatch([]*buf.Buffer{buf.As([]byte("repro"))}, []M.Socksaddr{destination}); err != nil {
		t.Fatal(err)
	}
	sink.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 32)
	n, from, err := sink.ReadFromUDP(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(b[:n]) != "repro" || !from.IP.IsLoopback() {
		t.Fatalf("unexpected datagram: %q from %v", b[:n], from)
	}
	t.Logf("unresolved FQDN %s arrived at IPv6 loopback %s", destination, sink.LocalAddr())
}

// The sing-box companion fix uses one-way address mapping: a domain request
// is sent to the resolved IP, while its response remains an IP for Surge.
//
//nolint:paralleltest // Subtests share a single UDP association intentionally.
func TestUDPDomainMappingRoundTrip(t *testing.T) {
	t.Parallel()
	source, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	domain := M.ParseSocksaddrHostPort("epdg.example.invalid", 500)
	mapped := bufio.NewUnidirectionalNATPacketConn(bufio.NewPacketConn(source), M.ParseSocksaddrHostPort("127.0.0.1", 500), domain)
	for _, batch := range []bool{false, true} {
		for _, literal := range []bool{false, true} {
			t.Run(fmt.Sprintf("batch=%t/literal=%t", batch, literal), func(t *testing.T) {
				sink, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
				if err != nil {
					t.Fatal(err)
				}
				defer sink.Close()
				target := domain
				target.Port = uint16(sink.LocalAddr().(*net.UDPAddr).Port)
				if literal {
					target = M.SocksaddrFromNet(sink.LocalAddr())
				}
				if batch {
					err = bufio.NewPacketBatchWriter(mapped).WritePacketBatch([]*buf.Buffer{buf.As([]byte("ike"))}, []M.Socksaddr{target})
				} else {
					err = mapped.WritePacket(buf.As([]byte("ike")), target)
				}
				if err != nil {
					t.Fatal(err)
				}
				sink.SetReadDeadline(time.Now().Add(time.Second))
				b := make([]byte, 32)
				n, from, err := sink.ReadFromUDP(b)
				if err != nil {
					t.Fatal(err)
				}
				if string(b[:n]) != "ike" {
					t.Fatal("payload corrupted")
				}
				if _, err = sink.WriteToUDP([]byte("reply"), from); err != nil {
					t.Fatal(err)
				}
				source.SetReadDeadline(time.Now().Add(time.Second))
				reply := buf.NewPacket()
				defer reply.Release()
				address, err := mapped.ReadPacket(reply)
				if err != nil {
					t.Fatal(err)
				}
				if !address.IsIP() || address.Port != target.Port || string(reply.Bytes()) != "reply" {
					t.Fatalf("invalid response %v %q", address, reply.Bytes())
				}
			})
		}
	}
}
