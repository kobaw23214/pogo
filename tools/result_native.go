package tools

// resultNativeModelView preserves native protocol structure while replacing
// encoded media with metadata in the text reader. The saved native record is
// untouched. Selecting a container such as /content must not accidentally pour
// an inline image or audio payload into the language model's text context.
func resultNativeModelView(value any) any {
	root, ok := value.(map[string]any)
	if !ok {
		return value
	}
	content, ok := root["content"].([]any)
	if !ok {
		return value
	}
	copyRoot := make(map[string]any, len(root))
	for key, item := range root {
		copyRoot[key] = item
	}
	blocks := make([]any, len(content))
	for index, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			blocks[index] = item
			continue
		}
		copyBlock := make(map[string]any, len(block))
		for key, field := range block {
			copyBlock[key] = field
		}
		if kind, _ := block["type"].(string); kind == "image" || kind == "audio" {
			if data, ok := block["data"].(string); ok {
				copyBlock["data"] = resultBinaryDirectory(len(data))
			}
		}
		if resource, ok := block["resource"].(map[string]any); ok {
			if blob, ok := resource["blob"].(string); ok {
				copyResource := make(map[string]any, len(resource))
				for key, field := range resource {
					copyResource[key] = field
				}
				copyResource["blob"] = resultBinaryDirectory(len(blob))
				copyBlock["resource"] = copyResource
			}
		}
		blocks[index] = copyBlock
	}
	copyRoot["content"] = blocks
	return copyRoot
}

func resultBinaryDirectory(encodedBytes int) map[string]any {
	return map[string]any{
		"omitted_binary": true, "encoded_bytes": encodedBytes,
		"reason": "Original media is retained in the saved native record, but is not decoded or delivered by this text-reading tool.",
	}
}
