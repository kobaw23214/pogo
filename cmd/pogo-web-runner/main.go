// pogo-web-runner is an optional private Docker adapter for managed websites.
// It must not be published on a host port or exposed to application containers.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yosukeno/pogo/webservice"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "web runner:", err)
		os.Exit(1)
	}
}
func run() error {
	var options webservice.DockerRunnerOptions
	listen := flag.String("listen", "127.0.0.1:9090", "private control listener (never publish a host port)")
	flag.StringVar(&options.SocketPath, "docker-socket", "/var/run/docker.sock", "administrator Docker socket")
	flag.StringVar(&options.APIVersion, "docker-api", "1.48", "Docker API version with gateway priority support")
	flag.StringVar(&options.Image, "image", "", "pinned Python runtime image ID or digest")
	flag.StringVar(&options.PythonPath, "python", "/opt/venv/bin/python3", "absolute Python path inside runtime image")
	flag.StringVar(&options.NodeImage, "node-image", "", "optional pinned Node.js standard-library runtime image ID or digest")
	flag.StringVar(&options.NodePath, "node", "/usr/local/bin/node", "absolute Node.js path inside Node runtime image")
	flag.StringVar(&options.NodePythonPath, "node-python", "/usr/bin/python3", "trusted supervisor Python path inside Node runtime image")
	flag.StringVar(&options.GatewayContainer, "gateway-container", "", "trusted gateway container ID/name")
	flag.StringVar(&options.Installation, "installation", "", "persistent installation identity")
	flag.StringVar(&options.Prefix, "prefix", "pogo-web", "resource name prefix")
	flag.StringVar(&options.StateDir, "state-dir", "/var/lib/pogo-web-runner", "private persistent runner state volume")
	flag.Int64Var(&options.RuntimeBytesLimit, "runtime-bytes-limit", 0, "runtime soft storage budget; zero uses 1 GiB")
	flag.Int64Var(&options.DataBytesLimit, "data-bytes-limit", 0, "per-service data soft storage budget; zero uses 512 MiB")
	flag.Int64Var(&options.OwnerDataBytesLimit, "owner-data-bytes-limit", 0, "owner data soft storage budget; zero uses 2 GiB")
	flag.Parse()
	token := os.Getenv("POGO_WEB_RUNNER_TOKEN")
	if len(token) < 32 {
		return fmt.Errorf("POGO_WEB_RUNNER_TOKEN must have at least 32 characters")
	}
	runner, err := webservice.NewDockerRunner(options)
	if err != nil {
		return err
	}
	defer runner.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: *listen, Handler: webservice.NewRunnerRPCHandler(runner, token), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	maintenanceDone := make(chan struct{})
	storageDone := make(chan struct{})
	go func() {
		defer close(storageDone)
		delay := 15 * time.Second
		for {
			started := time.Now()
			pass, stop := context.WithTimeout(ctx, 15*time.Second)
			err := runner.ReconcileStorage(pass)
			stop()
			if err != nil {
				log.Printf("web storage pass incomplete: elapsed_ms=%d error=%v", time.Since(started).Milliseconds(), err)
				delay *= 2
				if delay > 2*time.Minute {
					delay = 2 * time.Minute
				}
			} else {
				delay = 15 * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	go func() {
		defer close(maintenanceDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			pass, stop := context.WithTimeout(ctx, 15*time.Second)
			_ = runner.ReconcileOwned(pass)
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	finished := make(chan error, 1)
	go func() { finished <- server.ListenAndServe() }()
	select {
	case err := <-finished:
		cancel()
		<-maintenanceDone
		<-storageDone
		return err
	case <-ctx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	err = server.Shutdown(shutdown)
	<-maintenanceDone
	<-storageDone
	if err == context.DeadlineExceeded {
		_ = server.Close()
	}
	return err
}
