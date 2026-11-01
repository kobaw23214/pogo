package agent

import (
	"fmt"

	"github.com/yosukeno/pogo/tools"
)

// SetBrowserModelVision refreshes the browser declaration when the owning
// session changes models. Like SetClient, callers serialize it with runs and
// compaction. History and other tools retain their state.
func (a *Agent) SetBrowserModelVision(seesImages bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return fmt.Errorf("cannot change browser image mode during an active run")
	}
	tool, ok := a.registry.HostNativeTool("browser")
	if !ok {
		return nil
	}
	browser, ok := tool.(*tools.Browser)
	if !ok {
		return nil
	}
	mode := tools.BrowserImageNone
	if seesImages {
		mode = tools.BrowserImageDirect
	} else if helper, exists := a.registry.Get("analyze_image"); exists {
		if _, native := helper.(*tools.ImageAnalyze); native {
			mode = tools.BrowserImageHelper
		}
	}
	if browser.ImageMode != mode {
		browser.ImageMode = mode
		a.rebuildMCPView()
	}
	return nil
}
