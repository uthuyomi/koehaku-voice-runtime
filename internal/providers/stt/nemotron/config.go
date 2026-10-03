package nemotron

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	WorkerExecutable string
	Encoder          string
	Decoder          string
	Joiner           string
	Tokens           string
	Threads          int
	Language         string
	QueueChunks      int
	Performance      bool
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		WorkerExecutable: getenv("NEMOTRON_WORKER_EXECUTABLE"),
		Encoder:          getenv("NEMOTRON_ENCODER"),
		Decoder:          getenv("NEMOTRON_DECODER"),
		Joiner:           getenv("NEMOTRON_JOINER"),
		Tokens:           getenv("NEMOTRON_TOKENS"),
		Threads:          2,
		Language:         "auto",
		QueueChunks:      64,
	}
	if v := getenv("NEMOTRON_THREADS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 64 {
			return Config{}, fmt.Errorf("invalid NEMOTRON_THREADS")
		}
		c.Threads = n
	}
	if v := getenv("NEMOTRON_LANGUAGE"); v != "" {
		c.Language = v
	}
	if v := getenv("NEMOTRON_PERFORMANCE"); v == "1" || v == "true" {
		c.Performance = true
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Validate() error {
	for name, path := range map[string]string{
		"worker": c.WorkerExecutable, "encoder": c.Encoder, "decoder": c.Decoder,
		"joiner": c.Joiner, "tokens": c.Tokens,
	} {
		if path == "" {
			return fmt.Errorf("Nemotron %s path is required", name)
		}
		info, err := os.Stat(filepath.Clean(path))
		if err != nil || info.IsDir() {
			return fmt.Errorf("Nemotron %s path is invalid", name)
		}
	}
	if c.Threads < 1 || c.QueueChunks < 1 {
		return fmt.Errorf("invalid Nemotron limits")
	}
	if c.Language == "" {
		return fmt.Errorf("Nemotron language is required")
	}
	return nil
}
