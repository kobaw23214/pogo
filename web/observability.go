package web

import (
	"net/http"

	"github.com/yosukeno/pogo/observability"
)

// Both the Web agent and CLI use one lease-aware runtime. Observing outbound
// calls does not depend on enabling the incoming MCP serving surface.
func (s *Server) initObservability() error {
	var cfg MCPLogOptions
	if s.opts.MCPLog != nil {
		cfg = *s.opts.MCPLog
	}
	st, err := newMCPLogStore(cfg, s.opts.Logger)
	if err != nil {
		if s.opts.Logger != nil {
			s.opts.Logger.Print("observability: history unavailable; using memory")
		}
		cfg.DBPath = ""
		st, err = newMCPLogStore(cfg, s.opts.Logger)
		if err != nil {
			return err
		}
	}
	s.mcplog = st
	s.otlpConfigError = s.opts.OTLPConfigError
	if s.opts.OTLP != nil {
		s.otlp, err = observability.NewOTLPExporter(*s.opts.OTLP)
		if err != nil {
			s.otlpConfigError = true
			if s.opts.Logger != nil {
				s.opts.Logger.Print("observability: invalid OTLP configuration; export disabled")
			}
		}
	}
	opts := observability.Options{
		DB: st.db, Ring: cfg.Ring, MaxRows: cfg.MaxRows, RetainDays: cfg.RetainDays,
		DisableMCP: cfg.CallsOff, DisableSkill: cfg.SkillCallsOff,
	}
	if s.otlp != nil {
		opts.Sink = s.otlp
	}
	runtime, err := observability.Open(opts)
	if err != nil {
		if s.opts.Logger != nil {
			s.opts.Logger.Print("observability: metadata persistence unavailable; using memory")
		}
		opts.DB = nil
		runtime, err = observability.Open(opts)
		if err != nil {
			return err
		}
	}
	s.observation = runtime
	s.observationPersistent = opts.DB != nil
	s.mcpCalls, s.skillCalls = runtime.MCP, runtime.Skills
	s.mgr.SetMCPRecorder(runtime.MCPRecorder())
	s.mgr.SetSkillRecorder(runtime.SkillRecorder())
	return nil
}

func (s *Server) handleObservabilityStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	heartbeatErrors := int64(0)
	if s.observation != nil {
		heartbeatErrors = s.observation.HeartbeatErrors()
	}
	writeJSON(w, http.StatusOK, struct {
		MCP             bool                    `json:"mcp_enabled"`
		Skills          bool                    `json:"skills_enabled"`
		History         bool                    `json:"history_enabled"`
		HeartbeatErrors int64                   `json:"heartbeat_errors"`
		OTLPConfigError bool                    `json:"otlp_config_error"`
		OTLP            observability.OTLPStats `json:"otlp"`
	}{s.mcpCalls != nil, s.skillCalls != nil, s.observationPersistent,
		heartbeatErrors, s.otlpConfigError, s.otlp.Stats()})
}
