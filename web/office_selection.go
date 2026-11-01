package web

import "encoding/json"

// A selection read belongs to one live canvas executor. Neither a saved file
// version nor a successful earlier read can stand in for this binding.
type officeSelectionBinding struct {
	DocumentID    string  `json:"documentId"`
	InstanceID    string  `json:"instanceId"`
	CanvasVersion string  `json:"canvasVersion"`
	DirtyEpoch    *uint64 `json:"dirtyEpoch"`
}

func officeSelectionArguments(args json.RawMessage) (officeSelectionBinding, error) {
	var binding officeSelectionBinding
	if officeDecodeJSON(args, &binding) != nil || !officeOperationID.MatchString(binding.DocumentID) || !officeOperationID.MatchString(binding.InstanceID) || !officeVersionValidHash(binding.CanvasVersion) || binding.DirtyEpoch == nil {
		return binding, officeFail(400, "INVALID_ARGUMENT", "引用选区需要当前文档、编辑实例、画布版本和编辑轮次。")
	}
	return binding, nil
}

func officeSelectionMatchesLocked(d *officeDocumentSession, args json.RawMessage) error {
	binding, err := officeSelectionArguments(args)
	if err != nil {
		return err
	}
	if d.documentID != binding.DocumentID || d.instanceID != binding.InstanceID || d.baseHash != binding.CanvasVersion || d.dirtyEpoch != *binding.DirtyEpoch || d.closed || !d.canvasMounted {
		return officeFail(409, "SELECTION_CHANGED", "引用期间文档或编辑状态已改变，请重新选择内容后引用。")
	}
	return nil
}
