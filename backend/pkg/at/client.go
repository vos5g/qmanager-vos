package at

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Client handles thread-safe AT command execution
type Client struct {
	mu         sync.Mutex
	devicePath string
	lockFile   string
	timeout    time.Duration
}

// detectATDevice scans candidate serial ports if default path does not exist
func detectATDevice(initial string) string {
	if initial != "" {
		if _, err := os.Stat(initial); err == nil {
			return initial
		}
	}

	// Priority list for embedded modems and USB passthrough host ports
	candidates := []string{
		"/dev/smd11",       // Quectel internal SMD port (RM520N/RM500Q)
		"/dev/smd7",        // Quectel alternate SMD port
		"/dev/ttyUSB2",     // Quectel USB AT port
		"/dev/ttyUSB3",     // Qualcomm USB AT port
		"/dev/ttyUSB0",     // Standard USB serial
		"/dev/ttyACM0",     // ModemManager / CDC-ACM port
		"/dev/cdc-wdm0",    // QMI / MBIM control port
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	if initial != "" {
		return initial
	}
	return "/dev/smd11"
}

// NewClient initializes a new AT command client with port auto-discovery
func NewClient(devicePath string) *Client {
	resolvedPath := detectATDevice(devicePath)
	return &Client{
		devicePath: resolvedPath,
		lockFile:   "/tmp/qmanager_at.lock",
		timeout:    5 * time.Second,
	}
}

// Exec executes an AT command with thread-safety and mutex lock
func (c *Client) Exec(cmd string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Ensure command ends with CRLF
	cmd = strings.TrimSpace(cmd)
	if !strings.HasPrefix(strings.ToUpper(cmd), "AT") {
		return "", fmt.Errorf("invalid AT command: %s", cmd)
	}

	// On Linux modem/host, try executing via qcmd or direct device IO
	if runtime.GOOS == "linux" {
		resp, err := c.execOnModem(cmd)
		if err == nil {
			return resp, nil
		}
	}

	if runtime.GOOS != "linux" {
		return c.execMock(cmd) // development builds only
	}
	return "ERROR", fmt.Errorf("modem not reachable")
}

func (c *Client) execOnModem(cmd string) (string, error) {
	// vosclean build: the Compal RXM-G1 is driven only through qcmd (Quectel-AT
	// compatibility layer). No raw serial writes, no mock data.
	bin, err := exec.LookPath("qcmd")
	if err != nil {
		return "", fmt.Errorf("qcmd not found: %w", err)
	}
	out, err := exec.Command(bin, cmd).Output()
	res := strings.TrimSpace(string(out))
	if res == "" {
		if err == nil {
			err = fmt.Errorf("empty response")
		}
		return "", err
	}
	return res, nil
}

func (c *Client) execMock(cmd string) (string, error) {
	// Development mock responses
	cmdUpper := strings.ToUpper(cmd)
	switch {
	case strings.Contains(cmdUpper, "ATI"):
		return "Quectel\r\nRM520N-GL\r\nRevision: RM520NGLAAR01A07M4G\r\n\r\nOK", nil
	case strings.Contains(cmdUpper, "AT+QENG=\"SERVINGCELL\""):
		return "+QENG: \"servingcell\",\"NOCONN\",\"NR5G-SA\",\"TDD\",528,11,437000,627464,108,12,-11,-92,-11,15,0,0,0\r\n\r\nOK", nil
	default:
		return "OK", nil
	}
}
