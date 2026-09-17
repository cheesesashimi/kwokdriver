package builder

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"time"
)

//go:embed image
var buildContextFS embed.FS

func getBuildContext(dynamicFiles map[string][]byte) (io.ReadSeeker, string, error) {
	buf := bytes.NewBuffer([]byte{})

	// 3. Create the tar writer
	tw := tar.NewWriter(buf)

	defer tw.Close()

	for filename, content := range dynamicFiles {
		hdr := &tar.Header{
			Name:    filename,
			Mode:    0o755,
			Size:    int64(len(content)),
			ModTime: time.Time{}, // Purposely left empty for deterministic hashing.
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return nil, "", err
		}

		if _, err := tw.Write(content); err != nil {
			return nil, "", err
		}
	}

	// 4. Specify the subdirectory root to archive (use "." to archive everything embedded)
	searchRoot := "image"

	// 5. Walk through the embedded file system
	err := fs.WalkDir(buildContextFS, searchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get the metadata (FileInfo) required for the tar header
		info, err := d.Info()
		if err != nil {
			return err
		}

		// Create a standard tar header
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}

		// CRITICAL: FileInfoHeader only captures the base name.
		// You must rewrite header.Name to preserve the directory structure.
		//		header.Name = path

		// If it's a directory, append a trailing slash per tar specifications
		if d.IsDir() {
			header.Name += "/"
		}

		// Write the header to the archive
		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		// If it's a directory, there is no file content to copy
		if d.IsDir() {
			return nil
		}

		// Open the embedded file
		file, err := buildContextFS.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		// Copy the embedded file contents into the tar writer
		_, err = io.Copy(tw, file)
		return err
	})
	if err != nil {
		return nil, "", err
	}

	bufBytes := buf.Bytes()

	return bytes.NewReader(bufBytes), fmt.Sprintf("sha256:%x", sha256.Sum256(bufBytes)), nil
}
