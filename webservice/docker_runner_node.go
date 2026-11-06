package webservice

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"path"
)

// node-web intentionally has no installer/build service. A package.json may
// select ESM and contain metadata/scripts, but dependencies require a separately
// reviewed installer profile rather than silently disappearing at runtime.
func validateNodeArchive(archive []byte) error {
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return Fail("INVALID_PROJECT_ROOT", "invalid Node.js project archive")
		}
		if path.Base(h.Name) != "package.json" || h.Typeflag == tar.TypeDir {
			continue
		}
		var manifest map[string]json.RawMessage
		data, err := io.ReadAll(io.LimitReader(reader, 1<<20))
		if err != nil || h.Size > 1<<20 || json.Unmarshal(data, &manifest) != nil || manifest == nil {
			return Fail("INVALID_PROJECT_ROOT", "node-web requires a valid package.json smaller than 1 MiB")
		}
		for _, key := range []string{"dependencies", "devDependencies", "optionalDependencies"} {
			if raw, present := manifest[key]; present {
				var dependencies map[string]json.RawMessage
				if json.Unmarshal(raw, &dependencies) != nil || len(dependencies) > 0 {
					return Fail("DEPENDENCY_UNSUPPORTED", "node-web supports Node.js standard-library applications; npm dependencies and builds are unavailable")
				}
			}
		}
	}
}
