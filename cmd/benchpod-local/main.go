// Command benchpod-local runs the BenchPod desktop app on one computer: the agent loop, the
// BenchPod MCP server it starts, and the webapp, embedded in the binary.
//
// There is no sign-in. It listens on loopback only and refuses requests that did not come from
// a page it served (see guard.go). The model key stays in the OS keychain or a .env file and
// never reaches the page.
//
// Run it directly and it opens a browser tab. The desktop app runs it as a sidecar with
// -open=false -lifeline and shows the same page in its own window.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/embeddedci-com/benchpod-desktop/local"
)

var version = "dev"

// ReadyPrefix is printed once the server accepts connections; the desktop shell reads the URL
// from it.
const ReadyPrefix = "BENCHPOD_LOCAL_READY "

const defaultAddr = "127.0.0.1:7466"

func main() {
	var (
		addr     = flag.String("addr", envOr("BENCHPOD_LOCAL_ADDR", defaultAddr), "listen address; must be a loopback address")
		dataDir  = flag.String("data-dir", envOr("BENCHPOD_LOCAL_DATA_DIR", defaultDataDir()), "where settings, the conversation and the action log are kept")
		open     = flag.Bool("open", true, "open the app in the default browser once it is ready")
		lifeline = flag.Bool("lifeline", false, "exit when stdin closes (used by the desktop app, so the server never outlives it)")
		webDir   = flag.String("webapp", envOr("BENCHPOD_LOCAL_WEBAPP", ""), "serve the webapp from this directory instead of the embedded copy (development)")
		envFile  = flag.String("env-file", ".env", "a .env file with development keys (BENCHPOD_MODEL_API_KEY, OPENROUTER_API_KEY, BENCHPOD_MCP_COMMAND); missing is fine")
		showVer  = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger, *addr, *dataDir, *webDir, *envFile, *open, *lifeline); err != nil {
		logger.Error("benchpod-local", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, addr, dataDir, webDir, envFile string, open, lifeline bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if lifeline {
		// With the parent gone, a write to stdout or stderr would kill us before the MCP server
		// is stopped; ignored, the write fails quietly and shutdown carries on.
		signal.Ignore(syscall.SIGPIPE)
		ctx = withLifeline(ctx, os.Stdin, logger)
	}

	// The data folder's .env is for an installed app; the one in the working directory is
	// for development. The real environment wins over both.
	app, err := local.NewApp(dataDir, version, filepath.Join(dataDir, ".env"), envFile)
	if err != nil {
		return err
	}
	defer app.Close()

	web, err := webHandler(webDir)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", app.Handler())
	mux.Handle("/", web)

	ln, err := listen(addr, logger)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	srv := &http.Server{Handler: guard(mux), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	fmt.Println(ReadyPrefix + url)
	logger.Info("BenchPod Desktop is running", "url", url, "data", dataDir)
	if open {
		openBrowser(url)
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	return nil
}

// listen binds the address, refusing anything but loopback: the API has no sign-in, so a LAN
// bind would hand the pod to the network. A taken default port falls back to a free one.
func listen(addr string, logger *slog.Logger) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("listen address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("listen address %q is not a loopback address; the local app has no sign-in, so it only listens on this computer", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err == nil || addr != defaultAddr {
		return ln, err
	}
	logger.Warn("default port is in use, choosing another", "addr", addr, "err", err)
	return net.Listen("tcp", "127.0.0.1:0")
}

// withLifeline cancels ctx when stdin reaches EOF or reads "quit": the desktop shell's way of
// stopping us, and the safety net when it dies without asking.
func withLifeline(parent context.Context, stdin io.Reader, logger *slog.Logger) context.Context {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := stdin.Read(buf)
			if n > 0 && strings.Contains(string(buf[:n]), "quit") {
				logger.Info("asked to quit by the parent process")
				cancel()
				return
			}
			if err != nil {
				logger.Info("parent process went away (stdin closed)")
				cancel()
				return
			}
		}
	}()
	return ctx
}

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "benchpod-desktop")
	}
	return filepath.Join(os.TempDir(), "benchpod-desktop")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
