package brew

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// defaultCommandTimeout bounds each individual brew invocation, in addition to
// whatever deadline or cancellation the caller's context carries.
const defaultCommandTimeout = 60 * time.Second

// fallbackPaths are checked when brew is not on PATH. It is a variable so
// tests can disable the lookup.
var fallbackPaths = []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew", "/home/linuxbrew/.linuxbrew/bin/brew"}

// Client reads the local Homebrew installation. It only runs read-only
// commands and satisfies Loader.
type Client struct {
	path    string
	version string
	runner  Runner
	// timeout bounds each brew command; zero disables the adapter's own limit.
	timeout time.Duration
}

var _ Loader = (*Client)(nil)

// NewClient builds a Client for the brew at path using r. It is intended for
// tests; use Discover in production.
func NewClient(path string, r Runner) *Client {
	return &Client{path: path, runner: r, timeout: defaultCommandTimeout}
}

// Discover finds brew on PATH, then in the standard Homebrew prefixes, and
// records its version. It returns an error wrapping ErrNotFound if none exists.
func Discover(ctx context.Context) (*Client, error) {
	path, err := exec.LookPath("brew")
	if err != nil {
		path = ""
		for _, p := range fallbackPaths {
			if found, lerr := exec.LookPath(p); lerr == nil {
				path = found
				break
			}
		}
	}
	if path == "" {
		return nil, fmt.Errorf("%w: not on PATH or in standard prefixes", ErrNotFound)
	}

	c := NewClient(path, execRunner{})
	stdout, err := c.run(ctx, "--version")
	if err != nil {
		return nil, err
	}
	c.version = firstLine(stdout)
	return c, nil
}

// Path returns the brew executable path.
func (c *Client) Path() string { return c.path }

// Version returns the first line of `brew --version`, e.g. "Homebrew 7.0.7".
func (c *Client) Version() string { return c.version }

// Load reads installed formulae and casks and merges outdated state. Each brew
// command gets its own 60 second timeout; cancelling ctx aborts the load.
// Formulae and Casks are sorted by Name.
func (c *Client) Load(ctx context.Context) (Inventory, error) {
	infoOut, err := c.run(ctx, "info", "--json=v2", "--installed")
	if err != nil {
		return Inventory{}, err
	}
	outdatedOut, err := c.run(ctx, "outdated", "--json=v2")
	if err != nil {
		return Inventory{}, err
	}

	formulae, casks, err := parseInfo(infoOut)
	if err != nil {
		return Inventory{}, err
	}
	out, err := parseOutdated(outdatedOut)
	if err != nil {
		return Inventory{}, err
	}
	formulae, casks = merge(formulae, casks, out)

	return Inventory{
		BrewPath:    c.path,
		BrewVersion: c.version,
		Formulae:    formulae,
		Casks:       casks,
		LoadedAt:    time.Now(),
	}, nil
}

// run executes one brew command under the per-command timeout and classifies
// failures. It is the only place that prefixes errors with the command line.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	stdout, _, err := c.runner.Run(ctx, c.path, args...)
	if err != nil {
		if isUnsupportedOption(err) {
			return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
		}
		var ce *CommandError
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, fmt.Errorf("brew %s: %w", strings.Join(args, " "), err)
	}
	return stdout, nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
