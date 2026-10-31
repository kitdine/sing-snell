package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/sagernet/sing-snell/snellv6"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		panic("usage: udp_e2e /absolute/path/to/sing-box")
	}
	for _, mode := range []snellv6.Mode{snellv6.ModeDefault, snellv6.ModeUnshaped, snellv6.ModeUnsafeRaw} {
		if err := run(mode); err != nil {
			panic(err)
		}
	}
}
func run(mode snellv6.Mode) error {
	temp, err := os.MkdirTemp("", "snell-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)

	sink, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.2")})
	if err != nil {
		return err
	}
	defer sink.Close()
	go func() {
		b := make([]byte, 65536)
		for {
			n, a, e := sink.ReadFromUDP(b)
			if e != nil {
				return
			}
			sink.WriteToUDP(b[:n], a)
		}
	}()
	reserve, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	cfg := map[string]any{"log": map[string]any{"level": "debug", "timestamp": true}, "dns": map[string]any{"servers": []any{map[string]any{"type": "hosts", "tag": "hosts", "predefined": map[string]any{"epdg-cloud-test.invalid": []string{"127.0.0.2"}}}}}, "route": map[string]any{"default_domain_resolver": "hosts"}, "inbounds": []any{map[string]any{"type": "snell", "tag": "snell-test", "version": 6, "listen": "127.0.0.1", "listen_port": port, "psk": "cloud-e2e-test-only-psk", "mode": mode.String()}}, "outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}}}
	data, _ := json.Marshal(cfg)
	config := filepath.Join(temp, "config-"+mode.String()+".json")
	if err = os.WriteFile(config, data, 0600); err != nil {
		return err
	}
	binary, err := filepath.Abs(os.Args[1])
	if err != nil {
		return err
	}
	log, err := os.Create(filepath.Join(temp, "server-"+mode.String()+".log"))
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(binary, "run", "-c", config)
	cmd.Stdout = io.MultiWriter(log, os.Stderr)
	cmd.Stderr = io.MultiWriter(log, os.Stderr)
	if err = cmd.Start(); err != nil {
		return err
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	var tcp net.Conn
	for i := 0; i < 100; i++ {
		tcp, err = net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		log.Sync()
		contents, _ := os.ReadFile(log.Name())
		return fmt.Errorf("start server: %w\n%s", err, contents)
	}
	defer tcp.Close()
	tcp.SetDeadline(time.Now().Add(10 * time.Second))
	client, err := snellv6.NewClient(snellv6.ClientOptions{PSK: []byte("cloud-e2e-test-only-psk"), Mode: mode})
	if err != nil {
		return err
	}
	packet, err := client.DialPacketConn(tcp)
	if err != nil {
		return err
	}
	defer packet.Close()
	for _, host := range []string{"epdg-cloud-test.invalid", "127.0.0.2"} {
		for _, size := range []int{1, 136, 512, 1400, 4096} {
			target := M.ParseSocksaddrHostPort(host, uint16(sink.LocalAddr().(*net.UDPAddr).Port))
			payload := bytes.Repeat([]byte{byte(size % 251)}, size)
			b := buf.NewSize(131072)
			b.Resize(65536, 0)
			b.Write(payload)
			if err = packet.WritePacket(b, target); err != nil {
				return fmt.Errorf("%s %s size%d write: %w", mode, host, size, err)
			}
			reply := buf.NewPacket()
			from, err := packet.ReadPacket(reply)
			if err != nil {
				return fmt.Errorf("%s %s size%d read: %w", mode, host, size, err)
			}
			if from.Addr.String() != "127.0.0.2" || from.IsDomain() || from.Port != target.Port || !bytes.Equal(reply.Bytes(), payload) {
				return fmt.Errorf("bad response from %v length%d", from, reply.Len())
			}
			reply.Release()
			fmt.Printf("PASS mode=%s target=%s bytes=%d reply=%s\n", mode, host, size, from)
		}
	}
	return nil
}
