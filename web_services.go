package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/internal/webgateway"
	"github.com/yosukeno/pogo/webservice"
)

// Explicit opt-in; the Pogo process only talks to private adapters and never
// receives the Docker socket used by the separate runner.
func managedWebServicesFromEnv(change func(webservice.Record)) (*webservice.Manager, []string, func(), error) {
	if os.Getenv("POGO_WEB_SERVICE_ENABLED") != "1" {
		return nil, nil, func() {}, nil
	}
	root := strings.TrimSpace(os.Getenv("POGO_WEB_SERVICE_ROOT"))
	installation := strings.TrimSpace(os.Getenv("POGO_WEB_SERVICE_INSTALLATION"))
	endpoint := strings.TrimSpace(os.Getenv("POGO_WEB_SERVICE_RUNNER_URL"))
	token := os.Getenv("POGO_WEB_SERVICE_RUNNER_TOKEN")
	socket := strings.TrimSpace(os.Getenv("POGO_WEB_SERVICE_GATEWAY_SOCKET"))
	if !filepath.IsAbs(root) || installation == "" || endpoint == "" || token == "" || !filepath.IsAbs(socket) {
		return nil, nil, nil, errors.New("managed Web services require an absolute private root, installation ID, runner URL/token and absolute gateway socket")
	}
	runner, err := webservice.NewRunnerRPCClient(endpoint, token)
	if err != nil {
		return nil, nil, nil, err
	}
	gateway := webgateway.NewAdminClient(socket)
	manager, err := webservice.NewManager(webservice.Options{Root: root, Installation: installation, Runner: runner, Gateway: gateway, OnChange: change})
	if err != nil {
		_ = gateway.Close()
		return nil, nil, nil, err
	}
	cleanup := func() { _ = manager.Close(); _ = gateway.Close() }
	return manager, []string{root, filepath.Dir(socket)}, cleanup, nil
}
