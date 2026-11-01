package agent

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// Derived query/search views have a local SourceTool label, not the original
// document's source type. Only the protected native reader and its verified
// origin can make such a view material for the current-run citation review.
// Directories, omissions and host-only metadata never take this path.
func (a *Agent) citationDerivedRead(call, result llm.Block, source *Source) bool {
	if call.Name != tools.ToolResultReadName || a.registry == nil || a.sources == nil || result.IsError || source == nil || source.Type != SourceTool || source.SavedResult == nil {
		return false
	}
	reader, _ := a.registry.Get(call.Name)
	if _, native := reader.(*tools.ToolResultRead); !native {
		return false
	}
	var read tools.ToolResultReadDetails
	if json.Unmarshal(result.Details, &read) != nil || read.OriginalIsError || read.Omitted ||
		(read.View != "query" && read.View != "search") || read.View == "search" && !read.ScanComplete {
		return false
	}
	// Inspect the actual bounded model view. An original host page may contain
	// rows while the delivered budget receipt contains only their locator.
	var page struct {
		ResultID      string            `json:"result_id"`
		View          string            `json:"view"`
		PageDisplayed bool              `json:"page_displayed"`
		ScanComplete  bool              `json:"scan_complete"`
		Aggregate     json.RawMessage   `json:"aggregate"`
		Items         []json.RawMessage `json:"items"`
	}
	if json.Unmarshal([]byte(StripCiteHeader(result.Text)), &page) != nil || page.ResultID != read.ResultID || page.View != read.View || !page.ScanComplete {
		return false
	}
	material := read.View == "query" && len(page.Aggregate) > 0 && string(page.Aggregate) != "null"
	if page.PageDisplayed {
		for _, raw := range page.Items {
			var item struct {
				Text         string `json:"text"`
				OmittedValue bool   `json:"omitted_value"`
			}
			if json.Unmarshal(raw, &item) == nil && !item.OmittedValue && (read.View == "query" || item.Text != "") {
				material = true
			}
		}
	}
	if !material {
		return false
	}
	ref := source.SavedResult
	if ref.Origin == nil || ref.OriginAmbiguous || ref.ResultID != read.ResultID || ref.View != read.View || ref.Pointer != read.Pointer {
		return false
	}
	binding, known := a.sources.resultOrigin(read.ResultID)
	if !known || binding.ambiguous || binding.source == nil {
		return false
	}
	origin := binding.source
	if !sameResultSource(origin, ref.Origin) || origin.Label != ref.Origin.Label || origin.CallID != ref.Origin.CallID || origin.Time != ref.Origin.Time {
		return false
	}
	switch origin.Type {
	case SourceFileRead:
		return strings.HasPrefix(origin.Locator, "file://") && !a.citationInstructionPath(origin.Title)
	case SourceSearch, SourceInformation:
		return true
	case SourceWebPage:
		page, err := url.Parse(origin.Locator)
		return err == nil && page.Host != "" && (page.Scheme == "https" || page.Scheme == "http")
	case SourceMCP:
		original, _ := a.registry.Get(read.OriginalTool)
		mcp, ok := original.(*tools.MCPTool)
		return ok && mcp.IsReadOnly() && mcp.CatalogRole() != tools.MCPCatalogRoleDirectory
	}
	return false
}

func (a *Agent) citationResultViews(results []llm.Block, messageIndex int) []llm.Block {
	if a.toolOutputBudget.Disabled || len(a.toolResultViews) == 0 {
		return results
	}
	var actual []llm.Block
	for index, result := range results {
		if view, ok := a.resultViewAt(toolResultPosition{messageIndex, index}, result); ok {
			if actual == nil {
				actual = append([]llm.Block(nil), results...)
			}
			actual[index].Text, actual[index].ResultID = view.text, view.resultID
			if view.delivery != nil && !view.delivery.Delivered && !view.delivery.StatisticsDelivered {
				// This copy is only the material-read classifier. Keep the real
				// model/transcript receipt intact, but do not call its locator an
				// observed body. The same rule protects recovered text/json pages.
				actual[index].Text = ""
			}
		}
	}
	if actual == nil {
		return results
	}
	return actual
}
