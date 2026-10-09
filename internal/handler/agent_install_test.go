package handler

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/gin-gonic/gin"
)

func TestAgentDistributionVerifiedChecksumAndTamper(t *testing.T) {
	data := t.TempDir()
	packages := filepath.Join(data, "agent-packages")
	if err := os.MkdirAll(packages, 0700); err != nil {
		t.Fatal(err)
	}
	asset := "virtualis-agent-linux-amd64"
	body := []byte("\x7fELF" + strings.Repeat("fixture", 256))
	if err := os.WriteFile(filepath.Join(packages, asset), body, 0600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	manifest := filepath.Join(packages, "SHA256SUMS")
	h := &Handler{rt: runtime.New(data)}
	request := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/agent/binary?os=linux&arch=amd64"+query, nil)
		h.AgentBinary(c)
		return w
	}
	for _, line := range []string{digest + "  " + asset + "\n", digest + " *" + asset + "\n"} {
		if err := os.WriteFile(manifest, []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
		if w := request(""); w.Code != 200 || w.Body.String() != string(body) {
			t.Fatalf("verified binary: status %d", w.Code)
		}
		if w := request("&checksum=1"); w.Code != 200 || w.Body.String() != digest+"  "+asset+"\n" {
			t.Fatal("checksum endpoint mismatch")
		}
	}
	for _, line := range []string{strings.Repeat("0", 64) + "  " + asset + "\n", "invalid  " + asset + "\n", digest + "  other\n", digest + "  " + asset + "\n" + digest + "  " + asset + "\n"} {
		if err := os.WriteFile(manifest, []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
		if w := request(""); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("unverified package accepted: status %d", w.Code)
		}
		if w := request("&checksum=1"); w.Code != http.StatusServiceUnavailable {
			t.Fatal("checksum endpoint failed open")
		}
	}
}

func TestAgentInstallScriptDoesNotEmbedQueryTokenAndMatchesStandalone(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/agent/install.sh?master=https://fixture.invalid&token=fixture-must-not-be-embedded", nil)
	(&Handler{}).AgentInstallScript(c)
	if w.Code != 200 || strings.Contains(w.Body.String(), "fixture-must-not-be-embedded") {
		t.Fatal("installer embedded a query token")
	}
	canonical, err := os.ReadFile("../../deploy/install-agent.sh")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(string(canonical), "umask 077\n", "umask 077\nAGENT_MASTER_DEFAULT='https://fixture.invalid'\n", 1)
	if w.Body.String() != want {
		t.Fatal("generated and standalone installer contracts drifted")
	}
	for _, marker := range []string{"--token-file", "--allow-insecure", "-N2", "validate_mode", "SHA256SUMS"} {
		if !strings.Contains(w.Body.String(), marker) {
			t.Fatalf("missing %s", marker)
		}
	}
}

func TestAgentDistributionRejectsMissingChecksum(t *testing.T) {
	data := t.TempDir()
	packages := filepath.Join(data, "agent-packages")
	if err := os.MkdirAll(packages, 0700); err != nil {
		t.Fatal(err)
	}
	asset := "virtualis-agent-linux-amd64"
	if err := os.WriteFile(filepath.Join(packages, asset), []byte("\x7fELF"+strings.Repeat("fixture", 256)), 0600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{rt: runtime.New(data)}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/agent/binary?os=linux&arch=amd64", nil)
	h.AgentBinary(c)
	if w.Code == http.StatusOK {
		t.Fatal("unverified Agent binary was served without a checksum manifest")
	}
}
