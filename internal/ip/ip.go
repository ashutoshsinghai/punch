package ip

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

var lookupURLs = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
}

// Local returns the machine's outbound local IPv4 address (the IP used for
// LAN traffic). This is what peers on the same network should connect to.
func Local() (string, error) {
	// Dial an external address without sending any traffic — just to find
	// which local interface the OS would use.
	conn, err := net.Dial("udp4", "8.8.8.8:53")
	if err != nil {
		return "", fmt.Errorf("could not determine local IP: %w", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// Public returns the machine's public IPv4 address.
// Tries multiple providers; returns the first success.
func Public() (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}

	for _, url := range lookupURLs {
		ip, err := fetch(client, url)
		if err == nil {
			return ip, nil
		}
	}

	return "", fmt.Errorf("could not determine public IP (no network or all providers failed)")
}

func fetch(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}

	ip := strings.TrimSpace(string(body))
	if ip == "" {
		return "", fmt.Errorf("empty response from %s", url)
	}
	return ip, nil
}
