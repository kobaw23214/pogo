package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func preserveReloadCatalog(t *testing.T) {
	t.Helper()
	catalogMu.RLock()
	oldProviders, oldModels, oldDefault, oldVision := providers, catalog, defaultModel, visionModel
	catalogMu.RUnlock()
	t.Cleanup(func() {
		catalogMu.Lock()
		providers, catalog, defaultModel, visionModel = oldProviders, oldModels, oldDefault, oldVision
		catalogMu.Unlock()
	})
}

func reloadFixtureJSON(generation int) string {
	return fmt.Sprintf(`{"providers":{"local":{"base_url":"https://generation-%d.example/v1"}},"models":[{"id":"reload-model","provider":"local","context_window":%d,"max_tokens":%d,"subagent":true,"subagent_model":"reload-model","followup_model":"reload-model"}],"default":"reload-model","vision_model":"reload-model"}`, generation, generation*100000, generation*1000)
}

func TestReloadPublishesWholeCatalogGenerationsToConcurrentReaders(t *testing.T) {
	preserveReloadCatalog(t)
	path := filepath.Join(t.TempDir(), "providers.json")
	t.Setenv(PathEnv, path)
	if err := os.WriteFile(path, []byte(reloadFixtureJSON(1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Reload(); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	errors := make(chan error, 1)
	var readers sync.WaitGroup
	for range 6 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			report := func(err error) {
				select {
				case errors <- err:
				default:
				}
			}
			for {
				select {
				case <-stop:
					return
				default:
				}
				cfg, err := Resolve("")
				if err != nil {
					report(fmt.Errorf("reader saw missing catalog during reload: %w", err))
					return
				}
				generation := cfg.ContextWindow / 100000
				if (generation != 1 && generation != 2) || cfg.BaseURL != fmt.Sprintf("https://generation-%d.example/v1", generation) || cfg.MaxTokens != int64(generation*1000) {
					report(fmt.Errorf("resolved endpoint/model generation was mixed: %+v", cfg))
					return
				}
				if len(Catalog()) != 1 || DefaultModel() != "reload-model" || VisionModel() != "reload-model" || !Configured("local") || KeyEnv("local") != "" || SubagentModel("reload-model") != "reload-model" || FollowupModel("reload-model") != "reload-model" || len(SubagentPool()) != 1 {
					report(fmt.Errorf("reader observed empty or partial catalog"))
					return
				}
			}
		}()
	}
	var reloadErr error
	for i := range 80 {
		if reloadErr = os.WriteFile(path, []byte(reloadFixtureJSON(i%2+1)), 0600); reloadErr != nil {
			break
		}
		if _, reloadErr = Reload(); reloadErr != nil {
			break
		}
	}
	close(stop)
	readers.Wait()
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	select {
	case err := <-errors:
		t.Fatal(err)
	default:
	}
}

func TestInvalidReloadRetainsPreviouslyUsableCatalog(t *testing.T) {
	preserveReloadCatalog(t)
	path := filepath.Join(t.TempDir(), "providers.json")
	t.Setenv(PathEnv, path)
	if err := os.WriteFile(path, []byte(reloadFixtureJSON(1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Reload(); err != nil {
		t.Fatal(err)
	}
	before, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`{"providers":`, `{"providers":{},"models":[]}`, `{"providers":{},"models":[{"id":"reload-model","provider":"missing"}]}`} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Reload(); err == nil {
			t.Fatalf("invalid catalog accepted: %s", invalid)
		}
		after, err := Resolve("")
		if err != nil || !reflect.DeepEqual(before, after) || len(Catalog()) != 1 || DefaultModel() != "reload-model" || VisionModel() != "reload-model" {
			t.Fatalf("failed reload discarded or partly changed usable model: before=%+v after=%+v error=%v", before, after, err)
		}
	}
}
