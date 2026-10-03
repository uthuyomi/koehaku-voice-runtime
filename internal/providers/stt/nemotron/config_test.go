package nemotron

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigFromEnv(t *testing.T) {
	d := t.TempDir()
	paths := map[string]string{}
	for _, k := range []string{"NEMOTRON_WORKER_EXECUTABLE", "NEMOTRON_ENCODER", "NEMOTRON_DECODER", "NEMOTRON_JOINER", "NEMOTRON_TOKENS"} {
		p := filepath.Join(d, k)
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		paths[k] = p
	}
	paths["NEMOTRON_THREADS"] = "2"
	c, err := ConfigFromEnv(func(k string) string { return paths[k] })
	if err != nil || c.Threads != 2 || c.Language != "auto" {
		t.Fatalf("config=%+v err=%v", c, err)
	}
	paths["NEMOTRON_THREADS"] = "0"
	if _, err = ConfigFromEnv(func(k string) string { return paths[k] }); err == nil {
		t.Fatal("invalid threads accepted")
	}
}

func TestInvalidModelPath(t *testing.T) {
	_, err := ConfigFromEnv(func(k string) string {
		if k == "NEMOTRON_WORKER_EXECUTABLE" {
			return os.Args[0]
		}
		return "missing"
	})
	if err == nil {
		t.Fatal("missing model accepted")
	}
}
