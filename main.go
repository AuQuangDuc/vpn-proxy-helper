package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

type DialFunc func(network, addr string) (net.Conn, error)

var (
	localInterfaceAddr = flag.String(`i`, ``, `Out going Local Network Interface Address or Interface Name`)
	listen            = flag.String(`l`, `127.0.0.1:1080`, `Bind address. Eg: unix:/tmp/.unix-socket/socks5.sock`)
	debug             = flag.Bool(`debug`, false, `Enable debug logging`)
	updateInterval    = flag.Duration(`update-interval`, 2*time.Second, `Interface update check interval`)
	
	// Use typed atomic pointers for better performance and type safety
	dialFuncAtomic  atomic.Pointer[DialFunc]
	dialFunc6Atomic atomic.Pointer[DialFunc]
	lastIPv4        atomic.Pointer[net.IP]
	lastIPv6        atomic.Pointer[net.IP]
	
	logger *slog.Logger
)

// extractIP efficiently extracts IP from different address types
func extractIP(addr net.Addr) net.IP {
	switch v := addr.(type) {
	case *net.IPAddr:
		return v.IP
	case *net.IPNet:
		return v.IP
	default:
		return nil
	}
}

// isValidLocalIP checks if IP is valid for local binding
func isValidLocalIP(ip net.IP) bool {
	return ip != nil && !ip.IsLinkLocalUnicast() && !ip.IsLoopback()
}

func loadDialFunc() {
	var currentIPv4, currentIPv6 net.IP
	
	// Load current IPs once
	if ip := lastIPv4.Load(); ip != nil {
		currentIPv4 = *ip
	}
	if ip := lastIPv6.Load(); ip != nil {
		currentIPv6 = *ip
	}

	// Create dialers with optimized settings
	tcpDialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
		DualStack: false,
	}
	tcpDialerV6 := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
		DualStack: false,
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		logger.Error("failed to get network interfaces", "error", err)
		return
	}

	var newIPv4, newIPv6 net.IP
	targetIP := net.ParseIP(*localInterfaceAddr) // Parse once outside loop

	// Optimize interface iteration
	for _, iface := range ifaces {
		// Skip inactive interfaces
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		// Check if this is the target interface by name
		if iface.Name == *localInterfaceAddr {
			if ipv4, ipv6 := findValidIPs(addrs); ipv4 != nil || ipv6 != nil {
				newIPv4, newIPv6 = ipv4, ipv6
				break
			}
			continue
		}

		// Check if this interface has the target IP
		if targetIP != nil {
			for _, addr := range addrs {
				if ip := extractIP(addr); ip != nil && ip.Equal(targetIP) {
					if ip.To4() != nil {
						newIPv4 = ip
					} else {
						newIPv6 = ip
					}
					break
				}
			}
		}

		// Early exit if both IPs are found
		if newIPv4 != nil && newIPv6 != nil {
			break
		}
	}

	// Update IPv4 dialer if changed
	if newIPv4 != nil && !newIPv4.Equal(currentIPv4) {
		tcpDialer.LocalAddr = &net.TCPAddr{IP: newIPv4}
		dialFunc := DialFunc(tcpDialer.Dial)
		dialFuncAtomic.Store(&dialFunc)
		lastIPv4.Store(&newIPv4)
		logger.Info("updated local IPv4", "ip", newIPv4)
	}

	// Update IPv6 dialer if changed
	if newIPv6 != nil && !newIPv6.Equal(currentIPv6) {
		tcpDialerV6.LocalAddr = &net.TCPAddr{IP: newIPv6}
		dialFunc6 := DialFunc(tcpDialerV6.Dial)
		dialFunc6Atomic.Store(&dialFunc6)
		lastIPv6.Store(&newIPv6)
		logger.Info("updated local IPv6", "ip", newIPv6)
	}
}

// findValidIPs finds the first valid IPv4 and IPv6 addresses
func findValidIPs(addrs []net.Addr) (ipv4, ipv6 net.IP) {
	for _, addr := range addrs {
		ip := extractIP(addr)
		if !isValidLocalIP(ip) {
			continue
		}
		
		if ip.To4() != nil && ipv4 == nil {
			ipv4 = ip
		} else if ip.To4() == nil && ipv6 == nil {
			ipv6 = ip
		}
		
		// Early exit if both found
		if ipv4 != nil && ipv6 != nil {
			break
		}
	}
	return
}

func main() {
	flag.Parse()
	// Initialize logger based on debug flag
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))

	// Set up graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var ln net.Listener
	var err error

	if strings.HasPrefix(*listen, `unix:`) {
		unixFile := (*listen)[5:]
		// Remove existing socket file
		if err := os.Remove(unixFile); err != nil && !os.IsNotExist(err) {
			logger.Error("failed to remove existing unix socket", "file", unixFile, "error", err)
		}
		
		ln, err = net.Listen(`unix`, unixFile)
		if err == nil {
			if err := os.Chmod(unixFile, 0666); err != nil {
				logger.Warn("failed to set socket permissions", "file", unixFile, "error", err)
			}
			logger.Info("listening on unix socket", "path", unixFile)
		}
	} else {
		ln, err = net.Listen(`tcp`, *listen)
		if err == nil {
			logger.Info("listening on TCP", "address", ln.Addr().String())
		}
	}

	if err != nil {
		logger.Error("failed to listen", "address", *listen, "error", err)
		os.Exit(1)
	}

	logger.Info("SOCKS5 server starting", "address", *listen)
	logger.Info("usage", "url", "socks5://"+*listen)
	logger.Info("example", "command", "curl -x socks5://"+*listen+" https://1.1.1.1/cdn-cgi/trace")

	// Start interface monitoring in a separate goroutine
	go func() {
		ticker := time.NewTicker(*updateInterval)
		defer ticker.Stop()
		
		// Load initial dial functions
		loadDialFunc()
		
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				loadDialFunc()
			}
		}
	}()

	server := socks5.NewServer(
		socks5.WithDialAndRequest(func(ctx context.Context, network, addr string, request *socks5.Request) (net.Conn, error) {
			logger.Debug("new connection", "destination", addr, "network", network)

			var dialFunc *DialFunc
			
			if request.DestAddr.AddrType == statute.ATYPIPv6 {
				dialFunc = dialFunc6Atomic.Load()
				if dialFunc == nil {
					return nil, errors.New("no IPv6 dial function available")
				}
			} else {
				dialFunc = dialFuncAtomic.Load()
				if dialFunc == nil {
					return nil, errors.New("no IPv4 dial function available")
				}
			}

			return (*dialFunc)(network, addr)
		}),
	)

	// Start server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(ln)
	}()

	// Wait for shutdown signal or server error
	select {
	case <-ctx.Done():
		logger.Info("shutting down server...")
		if err := ln.Close(); err != nil {
			logger.Error("error closing listener", "error", err)
		}
	case err := <-errCh:
		if err != nil {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}

	logger.Info("server stopped")
}
