package main

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func getJSON(t *testing.T, h fiber.Handler, path string) map[string]interface{} {
	t.Helper()
	app := fiber.New()
	app.Get(path, h)
	resp, err := app.Test(httptest.NewRequest("GET", path, nil), 20000)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v (%s)", path, err, b)
	}
	return m
}

// Forge Solo for Linux runs no 1175 node. It sets MERGE_MINING_AVAILABLE=0, and the dashboard
// then hides every 1175 merge-mining control and the 1175 peer-port row. Everywhere else (Umbrel,
// Windows) the variable is unset and nothing changes.
func TestMergeMiningAvailableFlag(t *testing.T) {
	t.Setenv("MERGE_MINING_AVAILABLE", "")
	if cfg := getJSON(t, getPoolConfig, "/pool/config"); cfg["merge_mining_available"] != true {
		t.Fatalf("unset: merge_mining_available = %v, want true", cfg["merge_mining_available"])
	}
	if conn := getJSON(t, getConnectivity, "/connectivity"); conn["aux1175"] == nil || conn["bch2"] == nil {
		t.Fatalf("unset: connectivity %v, want both bch2 and aux1175", conn)
	}

	t.Setenv("MERGE_MINING_AVAILABLE", "0")
	if cfg := getJSON(t, getPoolConfig, "/pool/config"); cfg["merge_mining_available"] != false {
		t.Fatalf("0: merge_mining_available = %v, want false", cfg["merge_mining_available"])
	}
	conn := getJSON(t, getConnectivity, "/connectivity")
	if _, has := conn["aux1175"]; has || conn["bch2"] == nil {
		t.Fatalf("0: connectivity %v, want bch2 only", conn)
	}
}
