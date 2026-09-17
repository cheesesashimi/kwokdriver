package kwokdriver

import (
	"os"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

type testLogConsumerFactory struct{ t *testing.T }

func newTestLogConsumerFactory(t *testing.T) *testLogConsumerFactory {
	return &testLogConsumerFactory{t: t}
}

func (t *testLogConsumerFactory) newLogConsumerForComponent(name string) testcontainers.LogConsumer {
	return &testLogConsumer{t.t, name}
}

type testLogConsumer struct {
	t    *testing.T
	name string
}

func (t *testLogConsumer) Accept(l testcontainers.Log) {
	content := string(l.Content)
	t.t.Logf("%s: %s", t.name, strings.TrimSpace(content))
}

// FileLogConsumer implements testcontainers.LogConsumer
type fileLogConsumer struct {
	file *os.File
}

func newFileLogConsumer(filePath string) (*fileLogConsumer, error) {
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &fileLogConsumer{file: f}, nil
}

func (f *fileLogConsumer) Accept(l testcontainers.Log) {
	// Write the raw content of the log line to the file
	_, _ = f.file.Write(l.Content)
}

func (f *fileLogConsumer) Close() error {
	return f.file.Close()
}
